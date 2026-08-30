/*
 * health_monitor.c - Health / watchdog + stats snapshot (spec Sections 4, 14).
 */
#include "health_monitor.h"

#include <string.h>
#include "esp_timer.h"
#include "esp_task_wdt.h"
#include "esp_heap_caps.h"
#include "esp_log.h"

static const char *TAG = "health";

static struct {
    bool             inited;
    esp_timer_handle_t timer;
    health_report_t  report;
    portMUX_TYPE     mux;
} g = { .mux = portMUX_INITIALIZER_UNLOCKED };

static void snapshot(void *arg)
{
    (void)arg;
    health_report_t r;
    memset(&r, 0, sizeof(r));
    r.uptime_ms     = (uint64_t)(esp_timer_get_time() / 1000);
    r.free_heap     = (uint32_t)heap_caps_get_free_size(MALLOC_CAP_8BIT);
    r.min_free_heap = (uint32_t)heap_caps_get_minimum_free_size(MALLOC_CAP_8BIT);
    r.free_psram    = (uint32_t)heap_caps_get_free_size(MALLOC_CAP_SPIRAM);
    audio_manager_get_stats(&r.audio);

    portENTER_CRITICAL(&g.mux);
    g.report = r;
    portEXIT_CRITICAL(&g.mux);

    if (r.audio.streaming) {
        ESP_LOGD(TAG, "up=%llums heap=%u/%u psram=%u ovf=%llu qdepth=%u rtp=%llu/%llu",
                 (unsigned long long)r.uptime_ms, (unsigned)r.free_heap,
                 (unsigned)r.min_free_heap, (unsigned)r.free_psram,
                 (unsigned long long)r.audio.pcm_overflow,
                 (unsigned)r.audio.enc_count,
                 (unsigned long long)r.audio.rtp_packets_sent,
                 (unsigned long long)r.audio.rtp_send_errors);
    }
}

esp_err_t health_monitor_start(uint32_t wdt_timeout_ms, uint32_t period_ms)
{
    if (g.inited) return ESP_OK;
    if (period_ms == 0) period_ms = 1000;
    if (wdt_timeout_ms == 0) wdt_timeout_ms = 5000;

    /* Ensure the Task WDT exists. It may already be initialised by the SDK
     * (CONFIG_ESP_TASK_WDT_INIT); treat "already" as success. */
    esp_task_wdt_config_t wcfg = {
        .timeout_ms = wdt_timeout_ms,
        .idle_core_mask = 0,
        .trigger_panic = true,
    };
    esp_err_t err = esp_task_wdt_init(&wcfg);
    if (err == ESP_ERR_INVALID_STATE) {
        esp_task_wdt_reconfigure(&wcfg);
        err = ESP_OK;
    }
    if (err != ESP_OK) {
        ESP_LOGW(TAG, "task_wdt_init: %s", esp_err_to_name(err));
    }

    const esp_timer_create_args_t targs = {
        .callback = snapshot,
        .name = "health",
    };
    err = esp_timer_create(&targs, &g.timer);
    if (err != ESP_OK) return err;
    err = esp_timer_start_periodic(g.timer, (uint64_t)period_ms * 1000ull);
    if (err != ESP_OK) return err;

    g.inited = true;
    ESP_LOGI(TAG, "started: wdt=%ums period=%ums",
             (unsigned)wdt_timeout_ms, (unsigned)period_ms);
    return ESP_OK;
}

esp_err_t health_monitor_wdt_add(TaskHandle_t task)
{
    esp_err_t err = esp_task_wdt_add(task);
    if (err == ESP_ERR_INVALID_STATE || err == ESP_ERR_INVALID_ARG) {
        /* already subscribed / wdt not init: not fatal */
        return ESP_OK;
    }
    return err;
}

esp_err_t health_monitor_wdt_feed(void)
{
    return esp_task_wdt_reset();
}

esp_err_t health_monitor_wdt_remove(TaskHandle_t task)
{
    return esp_task_wdt_delete(task);
}

void health_monitor_get(health_report_t *out)
{
    if (!out) return;
    portENTER_CRITICAL(&g.mux);
    *out = g.report;
    portEXIT_CRITICAL(&g.mux);
}
