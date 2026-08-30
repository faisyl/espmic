/*
 * control_task.c - Persistent control connection (spec Sections 9, 10, 12, 14).
 */
#include "control_task.h"

#include <string.h>
#include <stdlib.h>
#include <stdio.h>
#include <errno.h>
#include <sys/socket.h>
#include <netdb.h>
#include <arpa/inet.h>

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "esp_log.h"
#include "esp_tls.h"
#include "cJSON.h"

#include "control_frame.h"
#include "audio_manager.h"
#include "health_monitor.h"
#include "nvs_config.h"

static const char *TAG = "control";

#define CONTROL_PROTOCOL_VERSION 1
#define RECONNECT_MIN_MS   1000
#define RECONNECT_MAX_MS   16000
#define READ_TIMEOUT_MS    5000     /* bounded read; drives keepalive ping */
#define KEEPALIVE_IDLE_MS  15000    /* send a device ping after this idle time */
#define CONTROL_LOSS_GRACE_MS 3000  /* stop an active stream after loss (spec 14) */

/* ---- connection abstraction (TLS via esp-tls, or plain TCP) --------------- */
typedef struct {
    bool        tls;
    esp_tls_t  *tls_ctx;
    int         sock;      /* plain TCP fd, or -1 */
} conn_t;

static struct {
    bool                 running;
    bool                 connected; /* session established (hello_ack seen) */
    TaskHandle_t         task;
    control_task_config_t cfg;
    cp_decoder_t         dec;
    conn_t               conn;
} g;

static void set_sock_rcvtimeo(int fd, int ms)
{
    struct timeval tv = { .tv_sec = ms / 1000, .tv_usec = (ms % 1000) * 1000 };
    setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
}

static esp_err_t conn_open(conn_t *c)
{
    memset(c, 0, sizeof(*c));
    c->sock = -1;

    if (g.cfg.tls_enabled) {
        esp_tls_cfg_t tcfg = { 0 };
        if (g.cfg.server_ca_pem && g.cfg.server_ca_pem_len) {
            tcfg.cacert_buf = (const unsigned char *)g.cfg.server_ca_pem;
            tcfg.cacert_bytes = g.cfg.server_ca_pem_len;
        } else {
            /* LAN mode: no CA verification. P3 TODO: pin/verify (spec Sec 15). */
            tcfg.skip_common_name = true;
        }
        tcfg.timeout_ms = 10000;

        c->tls_ctx = esp_tls_init();
        if (!c->tls_ctx) return ESP_FAIL;
        int r = esp_tls_conn_new_sync(g.cfg.host, (int)strlen(g.cfg.host),
                                      g.cfg.port, &tcfg, c->tls_ctx);
        if (r != 1) {
            ESP_LOGE(TAG, "TLS connect to %s:%u failed", g.cfg.host, g.cfg.port);
            esp_tls_conn_destroy(c->tls_ctx);
            c->tls_ctx = NULL;
            return ESP_FAIL;
        }
        int fd = -1;
        if (esp_tls_get_conn_sockfd(c->tls_ctx, &fd) == ESP_OK && fd >= 0) {
            set_sock_rcvtimeo(fd, READ_TIMEOUT_MS);
        }
        c->tls = true;
        return ESP_OK;
    }

    /* Plain TCP (controlled LAN only). */
    struct addrinfo hints = { .ai_family = AF_INET, .ai_socktype = SOCK_STREAM };
    struct addrinfo *res = NULL;
    char port_s[8];
    snprintf(port_s, sizeof(port_s), "%u", (unsigned)g.cfg.port);
    if (getaddrinfo(g.cfg.host, port_s, &hints, &res) != 0 || !res) {
        ESP_LOGE(TAG, "getaddrinfo %s failed", g.cfg.host);
        return ESP_FAIL;
    }
    int fd = socket(res->ai_family, res->ai_socktype, res->ai_protocol);
    if (fd < 0) { freeaddrinfo(res); return ESP_FAIL; }
    if (connect(fd, res->ai_addr, res->ai_addrlen) != 0) {
        ESP_LOGE(TAG, "TCP connect to %s:%u failed", g.cfg.host, g.cfg.port);
        close(fd);
        freeaddrinfo(res);
        return ESP_FAIL;
    }
    freeaddrinfo(res);
    set_sock_rcvtimeo(fd, READ_TIMEOUT_MS);
    c->sock = fd;
    c->tls = false;
    return ESP_OK;
}

static void conn_close(conn_t *c)
{
    if (c->tls_ctx) { esp_tls_conn_destroy(c->tls_ctx); c->tls_ctx = NULL; }
    if (c->sock >= 0) { close(c->sock); c->sock = -1; }
}

/* Returns bytes written, or -1 on error. Loops on partial writes. */
static int conn_write_all(conn_t *c, const uint8_t *buf, size_t len)
{
    size_t off = 0;
    while (off < len) {
        int w;
        if (c->tls) {
            w = esp_tls_conn_write(c->tls_ctx, buf + off, len - off);
            if (w == ESP_TLS_ERR_SSL_WANT_WRITE || w == ESP_TLS_ERR_SSL_WANT_READ) {
                vTaskDelay(pdMS_TO_TICKS(5));
                continue;
            }
        } else {
            w = send(c->sock, buf + off, len - off, 0);
        }
        if (w <= 0) return -1;
        off += (size_t)w;
    }
    return (int)off;
}

/* Returns >0 bytes, 0 on timeout (no data), -1 on error/closed. */
static int conn_read_some(conn_t *c, uint8_t *buf, size_t cap)
{
    int r;
    if (c->tls) {
        r = esp_tls_conn_read(c->tls_ctx, buf, cap);
        if (r == ESP_TLS_ERR_SSL_WANT_READ || r == ESP_TLS_ERR_SSL_WANT_WRITE) {
            return 0; /* timeout / retry */
        }
        if (r == 0) return -1; /* peer closed */
        return r; /* >0 data, <0 error */
    }
    r = recv(c->sock, buf, cap, 0);
    if (r < 0) {
        if (errno == EAGAIN || errno == EWOULDBLOCK) return 0; /* timeout */
        return -1;
    }
    if (r == 0) return -1; /* peer closed */
    return r;
}

/* ---- message senders ------------------------------------------------------ */

/* Frame a cJSON object and write it. Takes ownership of `root` (deletes it). */
static esp_err_t send_json(cJSON *root)
{
    esp_err_t ret = ESP_FAIL;
    char *json = cJSON_PrintUnformatted(root);
    cJSON_Delete(root);
    if (!json) return ESP_ERR_NO_MEM;

    size_t json_len = strlen(json);
    size_t framed_cap = CP_LENGTH_PREFIX_SIZE + json_len;
    uint8_t *framed = malloc(framed_cap);
    if (framed) {
        int n = cp_frame_encode((const uint8_t *)json, (uint32_t)json_len,
                                framed, framed_cap);
        if (n > 0 && conn_write_all(&g.conn, framed, (size_t)n) == n) {
            ret = ESP_OK;
        }
        free(framed);
    }
    free(json);
    return ret;
}

static void send_hello(void)
{
    cJSON *root = cJSON_CreateObject();
    cJSON_AddStringToObject(root, "type", "hello");
    cJSON_AddNumberToObject(root, "protocol", CONTROL_PROTOCOL_VERSION);
    cJSON_AddStringToObject(root, "device_id", g.cfg.device_id);
    cJSON_AddStringToObject(root, "firmware", g.cfg.firmware);
    cJSON *caps = cJSON_AddObjectToObject(root, "capabilities");
    cJSON *rates = cJSON_AddArrayToObject(caps, "sample_rates");
    cJSON_AddItemToArray(rates, cJSON_CreateNumber(48000));
    cJSON_AddNumberToObject(caps, "channels", 2);
    cJSON *codecs = cJSON_AddArrayToObject(caps, "codecs");
    cJSON_AddItemToArray(codecs, cJSON_CreateString("opus"));
    cJSON_AddBoolToObject(caps, "psram", true);
    send_json(root);
}

static void send_pong(void)
{
    cJSON *root = cJSON_CreateObject();
    cJSON_AddStringToObject(root, "type", "pong");
    send_json(root);
}

static void send_ping(void)
{
    cJSON *root = cJSON_CreateObject();
    cJSON_AddStringToObject(root, "type", "ping");
    send_json(root);
}

static void send_error(const char *request_id, const char *code, const char *msg)
{
    cJSON *root = cJSON_CreateObject();
    cJSON_AddStringToObject(root, "type", "error");
    if (request_id) cJSON_AddStringToObject(root, "request_id", request_id);
    cJSON_AddStringToObject(root, "code", code ? code : "error");
    cJSON_AddStringToObject(root, "message", msg ? msg : "");
    send_json(root);
}

static void send_stream_started(const char *request_id, const char *stream_id)
{
    cJSON *root = cJSON_CreateObject();
    cJSON_AddStringToObject(root, "type", "stream_started");
    if (request_id) cJSON_AddStringToObject(root, "request_id", request_id);
    if (stream_id) cJSON_AddStringToObject(root, "stream_id", stream_id);
    send_json(root);
}

/* Attach the aggregated stats block used by both status and stream_stopped. */
static void add_stats_block(cJSON *parent)
{
    health_report_t h;
    health_monitor_get(&h);
    cJSON *st = cJSON_AddObjectToObject(parent, "statistics");
    cJSON_AddNumberToObject(st, "uptime_ms", (double)h.uptime_ms);
    cJSON_AddNumberToObject(st, "free_heap", h.free_heap);
    cJSON_AddNumberToObject(st, "min_free_heap", h.min_free_heap);
    cJSON_AddNumberToObject(st, "free_psram", h.free_psram);
    cJSON_AddNumberToObject(st, "pcm_overflow", (double)h.audio.pcm_overflow);
    cJSON_AddNumberToObject(st, "pcm_high_water", h.audio.pcm_high_water);
    cJSON_AddNumberToObject(st, "encoded_drops", (double)h.audio.enc_drops);
    cJSON_AddNumberToObject(st, "encoded_high_water", h.audio.enc_high_water);
    cJSON_AddNumberToObject(st, "encoder_late", (double)h.audio.encoder_late);
    cJSON_AddNumberToObject(st, "capture_late", (double)h.audio.capture_late);
    cJSON_AddNumberToObject(st, "rtp_packets_sent", (double)h.audio.rtp_packets_sent);
    cJSON_AddNumberToObject(st, "rtp_bytes_sent", (double)h.audio.rtp_bytes_sent);
    cJSON_AddNumberToObject(st, "rtp_send_errors", (double)h.audio.rtp_send_errors);
    cJSON_AddNumberToObject(st, "rtp_ssrc", h.audio.rtp_ssrc);
}

static void send_status(const char *request_id)
{
    cJSON *root = cJSON_CreateObject();
    cJSON_AddStringToObject(root, "type", "status");
    if (request_id) cJSON_AddStringToObject(root, "request_id", request_id);
    cJSON_AddStringToObject(root, "state",
                            audio_manager_is_streaming() ? "STREAMING" : "IDLE");
    cJSON_AddStringToObject(root, "stream_id", audio_manager_stream_id());
    add_stats_block(root);
    send_json(root);
}

static void send_stream_stopped(const char *stream_id)
{
    cJSON *root = cJSON_CreateObject();
    cJSON_AddStringToObject(root, "type", "stream_stopped");
    if (stream_id) cJSON_AddStringToObject(root, "stream_id", stream_id);
    add_stats_block(root);
    send_json(root);
}

/* ---- command handlers ----------------------------------------------------- */

static void notify_sm(sm_event_t ev)
{
    if (g.cfg.on_state_event) g.cfg.on_state_event(ev, g.cfg.user);
}

/* Parse a start_stream body into audio_stream_params_t (spec Section 11). */
static bool parse_start_stream(const cJSON *root, audio_stream_params_t *p)
{
    memset(p, 0, sizeof(*p));

    const cJSON *sid = cJSON_GetObjectItemCaseSensitive(root, "stream_id");
    if (cJSON_IsString(sid) && sid->valuestring)
        strncpy(p->stream_id, sid->valuestring, sizeof(p->stream_id) - 1);

    const cJSON *dst = cJSON_GetObjectItemCaseSensitive(root, "destination");
    const cJSON *ip = cJSON_GetObjectItemCaseSensitive(dst, "ip");
    const cJSON *port = cJSON_GetObjectItemCaseSensitive(dst, "port");
    if (!cJSON_IsString(ip) || !ip->valuestring || !cJSON_IsNumber(port))
        return false;
    strncpy(p->dest_ip, ip->valuestring, sizeof(p->dest_ip) - 1);
    p->dest_port = (uint16_t)port->valuedouble;

    const cJSON *codec = cJSON_GetObjectItemCaseSensitive(root, "codec");
    const cJSON *name = cJSON_GetObjectItemCaseSensitive(codec, "name");
    if (!cJSON_IsString(name) || !name->valuestring ||
        strcmp(name->valuestring, "opus") != 0)
        return false;

    const cJSON *sr = cJSON_GetObjectItemCaseSensitive(codec, "sample_rate");
    const cJSON *ch = cJSON_GetObjectItemCaseSensitive(codec, "channels");
    const cJSON *fm = cJSON_GetObjectItemCaseSensitive(codec, "frame_ms");
    const cJSON *br = cJSON_GetObjectItemCaseSensitive(codec, "bitrate");
    const cJSON *vbr = cJSON_GetObjectItemCaseSensitive(codec, "vbr");
    const cJSON *fec = cJSON_GetObjectItemCaseSensitive(codec, "fec");
    const cJSON *dtx = cJSON_GetObjectItemCaseSensitive(codec, "dtx");
    p->sample_rate = cJSON_IsNumber(sr) ? (uint32_t)sr->valuedouble : 48000;
    p->channels    = cJSON_IsNumber(ch) ? (uint8_t)ch->valuedouble : 2;
    p->frame_ms    = cJSON_IsNumber(fm) ? (uint16_t)fm->valuedouble : 20;
    p->bitrate     = cJSON_IsNumber(br) ? (uint32_t)br->valuedouble : 0;
    p->vbr = cJSON_IsBool(vbr) ? cJSON_IsTrue(vbr) : true;
    p->fec = cJSON_IsBool(fec) ? cJSON_IsTrue(fec) : false;
    p->dtx = cJSON_IsBool(dtx) ? cJSON_IsTrue(dtx) : false;

    const cJSON *rtp = cJSON_GetObjectItemCaseSensitive(root, "rtp");
    const cJSON *pt = cJSON_GetObjectItemCaseSensitive(rtp, "payload_type");
    p->payload_type = cJSON_IsNumber(pt) ? (uint8_t)pt->valuedouble : 111;
    return true;
}

static const char *req_id_of(const cJSON *root)
{
    const cJSON *r = cJSON_GetObjectItemCaseSensitive(root, "request_id");
    return (cJSON_IsString(r) && r->valuestring) ? r->valuestring : NULL;
}

static void handle_message(const uint8_t *payload, uint32_t len)
{
    char type[32];
    if (cp_message_type(payload, len, type, sizeof(type)) < 0) {
        ESP_LOGW(TAG, "frame without a type; ignoring");
        return;
    }

    cJSON *root = cJSON_ParseWithLength((const char *)payload, len);
    if (!root) {
        ESP_LOGW(TAG, "malformed JSON (type=%s)", type);
        return;
    }
    const char *rid = req_id_of(root);

    if (strcmp(type, "hello_ack") == 0) {
        g.connected = true;
        ESP_LOGI(TAG, "session established (hello_ack)");
        notify_sm(SM_EV_CONTROL_CONNECTED);

    } else if (strcmp(type, "ping") == 0) {
        send_pong();

    } else if (strcmp(type, "pong") == 0) {
        /* liveness; nothing to do */

    } else if (strcmp(type, "start_stream") == 0) {
        audio_stream_params_t p;
        if (!parse_start_stream(root, &p)) {
            send_error(rid, "invalid_request", "malformed start_stream");
        } else {
            notify_sm(SM_EV_START_STREAM);
            esp_err_t e = audio_manager_start_stream(&p);
            if (e == ESP_OK) {
                notify_sm(SM_EV_STREAM_STARTED);
                send_stream_started(rid, p.stream_id);
            } else {
                /* Validation failed: remain IDLE (spec Section 11). */
                notify_sm(SM_EV_STOP_STREAM);
                send_error(rid, "invalid_config", "start_stream rejected");
            }
        }

    } else if (strcmp(type, "stop_stream") == 0) {
        char sid[64];
        strncpy(sid, audio_manager_stream_id(), sizeof(sid) - 1);
        sid[sizeof(sid) - 1] = '\0';
        audio_manager_stop_stream();
        notify_sm(SM_EV_STOP_STREAM);
        send_stream_stopped(sid);

    } else if (strcmp(type, "get_status") == 0) {
        send_status(rid);

    } else if (strcmp(type, "set_config") == 0) {
        const cJSON *br = cJSON_GetObjectItemCaseSensitive(root, "default_bitrate");
        if (cJSON_IsNumber(br)) {
            uint32_t bitrate = (uint32_t)br->valuedouble;
            audio_manager_apply_config(bitrate);
            nvs_config_set_u32("def_bitrate", bitrate);
        }
        const cJSON *host = cJSON_GetObjectItemCaseSensitive(root, "server_host");
        if (cJSON_IsString(host) && host->valuestring)
            nvs_config_set_str("server_host", host->valuestring);
        send_status(rid); /* echo new state */

    } else if (strcmp(type, "error") == 0) {
        const cJSON *m = cJSON_GetObjectItemCaseSensitive(root, "message");
        ESP_LOGW(TAG, "server error: %s",
                 (cJSON_IsString(m) && m->valuestring) ? m->valuestring : "?");

    } else {
        ESP_LOGW(TAG, "unhandled message type: %s", type);
    }

    cJSON_Delete(root);
}

/* control_frame decoder callback: one complete JSON frame. */
static void on_frame(const uint8_t *payload, uint32_t len, void *user)
{
    (void)user;
    handle_message(payload, len);
}

/* ---- connection lifecycle ------------------------------------------------- */

/* Run one connected session until the link drops. */
static void run_session(void)
{
    uint8_t rxbuf[1024];
    TickType_t last_rx = xTaskGetTickCount();

    cp_decoder_init(&g.dec);
    send_hello();

    while (g.running) {
        int r = conn_read_some(&g.conn, rxbuf, sizeof(rxbuf));
        if (r < 0) {
            ESP_LOGW(TAG, "read error / peer closed");
            break;
        }
        if (r == 0) {
            /* Idle: send a keepalive ping if silent too long. */
            if ((xTaskGetTickCount() - last_rx) >
                pdMS_TO_TICKS(KEEPALIVE_IDLE_MS)) {
                send_ping();
                last_rx = xTaskGetTickCount();
            }
            continue;
        }
        last_rx = xTaskGetTickCount();
        int rc = cp_decoder_push(&g.dec, rxbuf, (size_t)r, on_frame, NULL);
        if (rc == CP_ERR_OVERSIZE) {
            /* Oversized/malformed frame: drop the connection (spec Sec 7 test). */
            ESP_LOGE(TAG, "oversize control frame; dropping connection");
            break;
        }
    }
}

static void control_task_fn(void *arg)
{
    (void)arg;
    uint32_t backoff = RECONNECT_MIN_MS;

    while (g.running) {
        notify_sm(SM_EV_CONTROL_CONNECT);
        ESP_LOGI(TAG, "connecting to %s:%u (tls=%d)",
                 g.cfg.host, g.cfg.port, (int)g.cfg.tls_enabled);

        if (conn_open(&g.conn) != ESP_OK) {
            notify_sm(SM_EV_CONTROL_LOST);
            vTaskDelay(pdMS_TO_TICKS(backoff));
            backoff = backoff < RECONNECT_MAX_MS ? backoff * 2 : RECONNECT_MAX_MS;
            continue;
        }
        backoff = RECONNECT_MIN_MS;

        run_session();

        /* Session ended: connection lost. */
        g.connected = false;
        conn_close(&g.conn);
        notify_sm(SM_EV_CONTROL_LOST);

        /* Active stream + control loss: stop after a short grace (spec Sec 14). */
        if (audio_manager_is_streaming()) {
            ESP_LOGW(TAG, "control lost during stream; grace %d ms",
                     CONTROL_LOSS_GRACE_MS);
            vTaskDelay(pdMS_TO_TICKS(CONTROL_LOSS_GRACE_MS));
            if (g.running && !control_task_is_connected() &&
                audio_manager_is_streaming()) {
                audio_manager_stop_stream();
                notify_sm(SM_EV_STOP_STREAM);
            }
        }

        if (g.running) vTaskDelay(pdMS_TO_TICKS(backoff));
    }

    ESP_LOGI(TAG, "control task exiting");
    g.task = NULL;
    vTaskDelete(NULL);
}

esp_err_t control_task_start(const control_task_config_t *cfg)
{
    if (!cfg) return ESP_ERR_INVALID_ARG;
    if (g.running) return ESP_ERR_INVALID_STATE;

    memset(&g, 0, sizeof(g));
    g.cfg = *cfg;
    g.conn.sock = -1;
    g.running = true;

    int prio = cfg->task_priority > 0 ? cfg->task_priority : 5;
    BaseType_t ok;
    if (cfg->task_core >= 0) {
        ok = xTaskCreatePinnedToCore(control_task_fn, "control", 8192, NULL, prio,
                                     &g.task, cfg->task_core);
    } else {
        ok = xTaskCreate(control_task_fn, "control", 8192, NULL, prio, &g.task);
    }
    if (ok != pdPASS) {
        g.running = false;
        return ESP_ERR_NO_MEM;
    }
    return ESP_OK;
}

esp_err_t control_task_stop(void)
{
    if (!g.running) return ESP_OK;
    g.running = false;
    conn_close(&g.conn);
    for (int i = 0; i < 50 && g.task != NULL; i++) {
        vTaskDelay(pdMS_TO_TICKS(10));
    }
    return ESP_OK;
}

bool control_task_is_connected(void)
{
    return g.connected;
}
