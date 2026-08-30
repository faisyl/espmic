/*
 * health_monitor.h - Health / watchdog + stats snapshot (spec Sections 4, 14).
 *
 * Responsibilities:
 *   - keep the Task Watchdog enabled and let critical tasks subscribe to it;
 *   - periodically snapshot pipeline + system health (overflow counts, queue
 *     depths, RTP send count/errors, heap/PSRAM watermarks);
 *   - surface the latest snapshot for the `status` control message (spec
 *     Section 10) without the control task reaching into every subsystem.
 *
 * It must not restart healthy tasks (spec Section 4).
 */
#ifndef HEALTH_MONITOR_H
#define HEALTH_MONITOR_H

#include <stdint.h>
#include <stdbool.h>
#include "esp_err.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "audio_manager.h"

#ifdef __cplusplus
extern "C" {
#endif

/* Full health report handed to the status builder. */
typedef struct {
    uint64_t      uptime_ms;
    uint32_t      free_heap;
    uint32_t      min_free_heap;
    uint32_t      free_psram;
    int8_t        wifi_rssi;   /* dBm of the associated AP; 0 when not connected */
    audio_stats_t audio;
} health_report_t;

/*
 * Initialise the Task Watchdog (spec Section 14: watchdog must remain enabled)
 * with the given timeout, and start the periodic snapshot timer.
 * `period_ms` == 0 uses a 1000 ms default.
 */
esp_err_t health_monitor_start(uint32_t wdt_timeout_ms, uint32_t period_ms);

/* Subscribe / feed / unsubscribe a task to the Task Watchdog. Wrappers over
 * esp_task_wdt so callers do not depend on the header directly. */
esp_err_t health_monitor_wdt_add(TaskHandle_t task);
esp_err_t health_monitor_wdt_feed(void);
esp_err_t health_monitor_wdt_remove(TaskHandle_t task);

/* Copy the most recent health snapshot. */
void health_monitor_get(health_report_t *out);

#ifdef __cplusplus
}
#endif

#endif /* HEALTH_MONITOR_H */
