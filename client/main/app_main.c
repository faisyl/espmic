/*
 * ESP32-S3 Network Audio Device — Application Entry Point (P3 wire-up).
 *
 * Boots and wires the full pipeline and drives the spec Section 12 device state
 * machine from real connectivity events:
 *
 *   NVS -> nvs_config -> device_state(BOOT) -> health_monitor(+WDT)
 *       -> audio_manager_init -> wifi_manager_init -> wifi_manager_start
 *       -> (on got-IP) control_task_start
 *
 * Wi-Fi + control callbacks feed sm_handle_event() so the observable state
 * follows: BOOT -> [PROVISIONING] -> WIFI_CONNECTING -> WIFI_CONNECTED
 *          -> CONTROL_CONNECTING -> IDLE -> STREAM_STARTING -> STREAMING -> IDLE,
 * plus the any-state WIFI_LOST / CONTROL_LOST / FATAL / RECOVER overrides.
 *
 * See ESP32_Audio_Device_Specification.md (esp. Sections 12, 14, 16).
 */

#include <stdio.h>
#include <string.h>

#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "freertos/task.h"
#include "esp_log.h"
#include "esp_system.h"
#include "nvs_flash.h"

#include "device_state.h"
#include "nvs_config.h"
#include "audio_manager.h"
#include "health_monitor.h"
#include "wifi_manager.h"
#include "control_task.h"

static const char *TAG = "app_main";

#define FIRMWARE_VERSION   "1.0.0"
#define WDT_TIMEOUT_MS     8000   /* < CONFIG_ESP_TASK_WDT_TIMEOUT_S (10 s) */
#define HEALTH_PERIOD_MS   1000
#define FATAL_REBOOT_MS    3000

/* ---- device state machine holder (thread-safe) --------------------------- */
/*
 * sm_handle_event() is a pure function; app_main owns the single live state and
 * serialises event dispatch from the Wi-Fi event context and the control task
 * context behind one mutex. All connectivity side effects (starting the control
 * task, rebooting on FATAL) are driven from here.
 */
static struct {
    sm_state_t        state;
    SemaphoreHandle_t lock;
    bool              control_started;
    device_config_t   cfg;
} s;

static void sm_lock(void)   { if (s.lock) xSemaphoreTake(s.lock, portMAX_DELAY); }
static void sm_unlock(void) { if (s.lock) xSemaphoreGive(s.lock); }

/* Apply one event through the pure transition function and log any change. */
static sm_state_t sm_dispatch(sm_event_t ev)
{
    sm_lock();
    sm_state_t old = s.state;
    sm_state_t nw  = sm_handle_event(old, ev);
    s.state = nw;
    sm_unlock();
    if (nw != old) {
        ESP_LOGI(TAG, "state %s --%s--> %s", sm_state_name(old),
                 sm_event_name(ev), sm_state_name(nw));
    } else {
        ESP_LOGD(TAG, "event %s ignored in %s", sm_event_name(ev),
                 sm_state_name(old));
    }
    return nw;
}

/*
 * Force PROVISIONING. Spec Section 12: PROVISIONING is only entered when
 * credentials are absent or an explicit reset is requested; both surface as the
 * wifi_manager provisioning-started callback. Because the nominal boot path has
 * already advanced BOOT->WIFI_CONNECTING (BOOT_DONE), we set this state directly
 * rather than trying to route a second event through the pure SM. Design note in
 * client/P3_NOTES.md.
 */
static void sm_force_provisioning(void)
{
    sm_lock();
    sm_state_t old = s.state;
    s.state = SM_STATE_PROVISIONING;
    sm_unlock();
    ESP_LOGI(TAG, "state %s ==> PROVISIONING (credentials absent/reset)",
             sm_state_name(old));
}

static void go_fatal(const char *why)
{
    ESP_LOGE(TAG, "FATAL: %s — entering ERROR, rebooting in %d ms", why,
             FATAL_REBOOT_MS);
    sm_dispatch(SM_EV_FATAL);
    vTaskDelay(pdMS_TO_TICKS(FATAL_REBOOT_MS));
    esp_restart();
}

/* ---- control task callback ----------------------------------------------- */
/* control_task already emits the correct sm_event_t sequence
 * (CONTROL_CONNECT / CONTROL_CONNECTED / START_STREAM / STREAM_STARTED /
 * STOP_STREAM / CONTROL_LOST); we just funnel them into the shared SM. */
static void on_control_event(sm_event_t ev, void *user)
{
    (void)user;
    sm_dispatch(ev);
}

static void start_control_task(void)
{
    if (s.control_started) return;

    control_task_config_t ccfg = { 0 };
    strncpy(ccfg.host, s.cfg.server_host, sizeof(ccfg.host) - 1);
    ccfg.port        = s.cfg.server_port;
    ccfg.tls_enabled = s.cfg.control_tls_enabled;
    strncpy(ccfg.device_id, s.cfg.device_id, sizeof(ccfg.device_id) - 1);
    strncpy(ccfg.firmware, FIRMWARE_VERSION, sizeof(ccfg.firmware) - 1);
    /* CA/pinning is resolved inside control_task from NVS (hardened path) or
     * falls back to LAN mode; app_main does not need to supply a PEM here. */
    ccfg.server_ca_pem     = NULL;
    ccfg.server_ca_pem_len = 0;
    ccfg.on_state_event    = on_control_event;
    ccfg.user              = NULL;
    ccfg.task_priority     = 5;
    ccfg.task_core         = -1;

    esp_err_t err = control_task_start(&ccfg);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "control_task_start: %s", esp_err_to_name(err));
        return; /* not fatal: retried on the next got-IP */
    }
    s.control_started = true;
    ESP_LOGI(TAG, "control task started -> %s:%u (tls=%d)",
             s.cfg.server_host, s.cfg.server_port,
             (int)s.cfg.control_tls_enabled);
}

/* ---- wifi manager callbacks ---------------------------------------------- */
static void on_got_ip(uint32_t ip4, void *user)
{
    (void)ip4; (void)user;
    sm_dispatch(SM_EV_WIFI_CONNECTED);
    /* Start the control connection once we first have an IP; on later
     * reconnects the control task's own backoff loop resumes on its own. */
    start_control_task();
}

static void on_disconnected(void *user)
{
    (void)user;
    /* Any networked state -> WIFI_CONNECTING (no-op in BOOT/PROVISIONING). */
    sm_dispatch(SM_EV_WIFI_LOST);
}

static void on_provisioning_started(void *user)
{
    (void)user;
    sm_force_provisioning();
}

static void on_provisioned(void *user)
{
    (void)user;
    /* PROVISIONING -> WIFI_CONNECTING. */
    sm_dispatch(SM_EV_PROVISIONED);
}

/* ---- boot ---------------------------------------------------------------- */
void app_main(void)
{
    ESP_LOGI(TAG, "ESP32-S3 Network Audio Device starting (fw %s)...",
             FIRMWARE_VERSION);

    /* 1. NVS — required for Wi-Fi credentials and persistent config. */
    esp_err_t ret = nvs_flash_init();
    if (ret == ESP_ERR_NVS_NO_FREE_PAGES ||
        ret == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        ESP_ERROR_CHECK(nvs_flash_erase());
        ret = nvs_flash_init();
    }
    ESP_ERROR_CHECK(ret);

    /* 2. Load persistent configuration (spec Section 16). */
    nvs_config_defaults(&s.cfg);
    esp_err_t cerr = nvs_config_load(&s.cfg);
    if (cerr != ESP_OK) {
        ESP_LOGW(TAG, "nvs_config_load: %s (using defaults)",
                 esp_err_to_name(cerr));
    }
    ESP_LOGI(TAG, "config: id=%s host=%s:%u tls=%d br=%u i2s(bclk=%d ws=%d din=%d)",
             s.cfg.device_id, s.cfg.server_host, s.cfg.server_port,
             (int)s.cfg.control_tls_enabled, (unsigned)s.cfg.default_bitrate,
             (int)s.cfg.i2s_bclk_gpio, (int)s.cfg.i2s_ws_gpio,
             (int)s.cfg.i2s_din_gpio);

    /* 3. Device state machine at BOOT. */
    s.lock = xSemaphoreCreateMutex();
    if (!s.lock) { ESP_ERROR_CHECK(ESP_ERR_NO_MEM); }
    s.state = SM_STATE_BOOT;
    s.control_started = false;

    /* 4. Audio pipeline manager (board + codec defaults). Owns the tasks that
     *    subscribe to the watchdog while streaming. */
    audio_manager_config_t acfg = {
        .i2s_bclk_gpio  = (int)s.cfg.i2s_bclk_gpio,
        .i2s_ws_gpio    = (int)s.cfg.i2s_ws_gpio,
        .i2s_din_gpio   = (int)s.cfg.i2s_din_gpio,
        .default_bitrate = s.cfg.default_bitrate,
        .complexity     = 6,
    };
    if (audio_manager_init(&acfg) != ESP_OK) {
        go_fatal("audio_manager_init failed");
        return;
    }

    /* 5. Wi-Fi manager (drives connectivity events into the SM). */
    wifi_manager_config_t wcfg = {
        .on_got_ip              = on_got_ip,
        .on_disconnected        = on_disconnected,
        .on_provisioning_started = on_provisioning_started,
        .on_provisioned         = on_provisioned,
        .user                   = NULL,
    };
    strncpy(wcfg.service_name, "PROV_ESP32", sizeof(wcfg.service_name) - 1);
    /* pop left empty => open provisioning (SECURITY_0). The server auth secret
     * is never exposed via provisioning (spec Section 13). */
    if (wifi_manager_init(&wcfg) != ESP_OK) {
        go_fatal("wifi_manager_init failed");
        return;
    }

    /* 6. Health monitor: enable + reconfigure the Task WDT (spec Section 14:
     *    watchdog must remain enabled) and start the 1 s stats snapshot. */
    if (health_monitor_start(WDT_TIMEOUT_MS, HEALTH_PERIOD_MS) != ESP_OK) {
        ESP_LOGW(TAG, "health_monitor_start failed; continuing");
    }

    /* 7. Begin connecting. BOOT_DONE moves BOOT->WIFI_CONNECTING; if the driver
     *    finds no credentials it raises the provisioning-started callback and we
     *    reflect PROVISIONING. */
    sm_dispatch(SM_EV_BOOT_DONE);
    if (wifi_manager_start() != ESP_OK) {
        go_fatal("wifi_manager_start failed");
        return;
    }

    ESP_LOGI(TAG, "boot wiring complete; state=%s", sm_state_name(s.state));
    /* app_main returns; all further work runs in the wifi/event, control, and
     * pipeline tasks. The main task is intentionally not WDT-subscribed. */
}
