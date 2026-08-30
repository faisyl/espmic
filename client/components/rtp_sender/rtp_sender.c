/*
 * rtp_sender.c - RTP/UDP send task (spec Sections 6, 8, 14).
 */
#include "rtp_sender.h"

#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>

#include "freertos/task.h"
#include "esp_task_wdt.h"
#include "esp_log.h"
#include "rtp_packet.h"

static const char *TAG = "rtp_sender";

/* Bounded wait for the queue lock (spec Section 14: no indefinite waits). */
#define RTP_LOCK_TIMEOUT_MS 50

/* One RTP packet = 12-byte header + one Opus packet. Bound the payload to keep
 * the datagram within a typical MTU. */
#define RTP_MAX_PAYLOAD 1500
#define RTP_SEND_BUF    (RTP_HEADER_SIZE + RTP_MAX_PAYLOAD)

struct rtp_sender_ctx {
    rtp_sender_config_t cfg;
    rtp_state_t         rtp;
    int                 sock;
    TaskHandle_t        task;
    volatile bool       running;
    volatile bool       first_packet;
    rtp_sender_stats_t  stats;
};

static void rtp_task(void *arg)
{
    struct rtp_sender_ctx *ctx = (struct rtp_sender_ctx *)arg;
    static uint8_t opus[RTP_MAX_PAYLOAD];
    static uint8_t out[RTP_SEND_BUF];

    ESP_LOGI(TAG, "rtp task started -> %s:%u", ctx->cfg.dest_ip,
             (unsigned)ctx->cfg.dest_port);

    /* Watchdog-subscribe this critical loop (spec Section 14). */
    esp_task_wdt_add(NULL);

    while (ctx->running) {
        esp_task_wdt_reset();

        size_t len = 0;
        int popped = 0;
        if (ctx->cfg.queue_lock) {
            if (xSemaphoreTake(ctx->cfg.queue_lock,
                               pdMS_TO_TICKS(RTP_LOCK_TIMEOUT_MS)) != pdTRUE) {
                vTaskDelay(pdMS_TO_TICKS(5)); /* contended; retry, never block */
                continue;
            }
        }
        popped = eq_pop(ctx->cfg.queue, opus, sizeof(opus), &len);
        if (ctx->cfg.queue_lock) xSemaphoreGive(ctx->cfg.queue_lock);

        if (popped != 1) {
            /* Queue empty (or head too big): bounded wait, then retry. */
            vTaskDelay(pdMS_TO_TICKS(5));
            continue;
        }

        int marker = ctx->first_packet ? 1 : 0; /* marker may be 1 on first pkt */
        int total = rtp_serialize(&ctx->rtp, opus, len, marker, out, sizeof(out));
        if (total < 0) {
            ctx->stats.send_errors++;
            ESP_LOGW(TAG, "rtp_serialize err=%d len=%u", total, (unsigned)len);
            continue;
        }
        ctx->first_packet = false;

        ssize_t sent = send(ctx->sock, out, (size_t)total, 0);
        if (sent < 0) {
            ctx->stats.send_errors++;
            /* Bounded retry semantics: count and continue, never block I2S. */
            continue;
        }
        ctx->stats.packets_sent++;
        ctx->stats.bytes_sent += (uint64_t)sent;
        ctx->stats.last_sequence = ctx->rtp.sequence;   /* next-to-send */
        ctx->stats.last_timestamp = ctx->rtp.timestamp;
    }

    esp_task_wdt_delete(NULL);
    ESP_LOGI(TAG, "rtp task exiting: sent=%llu errs=%llu",
             (unsigned long long)ctx->stats.packets_sent,
             (unsigned long long)ctx->stats.send_errors);
    ctx->task = NULL;
    vTaskDelete(NULL);
}

esp_err_t rtp_sender_start(const rtp_sender_config_t *cfg,
                           rtp_sender_handle_t *out)
{
    if (!cfg || !out || !cfg->queue) return ESP_ERR_INVALID_ARG;

    struct rtp_sender_ctx *ctx = calloc(1, sizeof(*ctx));
    if (!ctx) return ESP_ERR_NO_MEM;
    ctx->cfg = *cfg;
    ctx->sock = -1;
    ctx->first_packet = true;

    uint8_t pt = cfg->payload_type ? cfg->payload_type : RTP_DEFAULT_PAYLOAD_TYPE;
    rtp_init(&ctx->rtp, pt, cfg->ssrc_seed);
    ctx->stats.ssrc = ctx->rtp.ssrc;

    /* Connected UDP socket so we can use send() and get async ICMP errors. */
    ctx->sock = socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP);
    if (ctx->sock < 0) {
        ESP_LOGE(TAG, "socket() failed");
        free(ctx);
        return ESP_FAIL;
    }

    struct sockaddr_in dst = { 0 };
    dst.sin_family = AF_INET;
    dst.sin_port = htons(cfg->dest_port);
    if (inet_pton(AF_INET, cfg->dest_ip, &dst.sin_addr) != 1) {
        ESP_LOGE(TAG, "bad dest ip: %s", cfg->dest_ip);
        close(ctx->sock);
        free(ctx);
        return ESP_ERR_INVALID_ARG;
    }
    if (connect(ctx->sock, (struct sockaddr *)&dst, sizeof(dst)) < 0) {
        ESP_LOGE(TAG, "connect() failed");
        close(ctx->sock);
        free(ctx);
        return ESP_FAIL;
    }

    /* Non-blocking-ish send: bound how long a send may stall. */
    struct timeval tv = { .tv_sec = 0, .tv_usec = 100000 }; /* 100 ms */
    setsockopt(ctx->sock, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof(tv));

    ctx->running = true;
    int prio = ctx->cfg.task_priority > 0 ? ctx->cfg.task_priority
                                          : (configMAX_PRIORITIES - 5);
    BaseType_t ok;
    if (ctx->cfg.task_core >= 0) {
        ok = xTaskCreatePinnedToCore(rtp_task, "rtp", 4096, ctx, prio,
                                     &ctx->task, ctx->cfg.task_core);
    } else {
        ok = xTaskCreate(rtp_task, "rtp", 4096, ctx, prio, &ctx->task);
    }
    if (ok != pdPASS) {
        ctx->running = false;
        close(ctx->sock);
        free(ctx);
        return ESP_ERR_NO_MEM;
    }

    *out = ctx;
    ESP_LOGI(TAG, "started: pt=%u ssrc=0x%08x -> %s:%u", pt,
             (unsigned)ctx->rtp.ssrc, cfg->dest_ip, (unsigned)cfg->dest_port);
    return ESP_OK;
}

esp_err_t rtp_sender_stop(rtp_sender_handle_t h)
{
    if (!h) return ESP_ERR_INVALID_ARG;
    h->running = false;
    for (int i = 0; i < 20 && h->task != NULL; i++) {
        vTaskDelay(pdMS_TO_TICKS(10));
    }
    if (h->sock >= 0) {
        close(h->sock);
        h->sock = -1;
    }
    free(h);
    return ESP_OK;
}

void rtp_sender_get_stats(rtp_sender_handle_t h, rtp_sender_stats_t *out)
{
    if (!h || !out) return;
    *out = h->stats;
}
