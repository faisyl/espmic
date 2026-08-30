/*
 * ESP32-S3 Network Audio Device — Application Entry Point
 *
 * Initialises all subsystems and launches FreeRTOS tasks:
 *   - Wi-Fi manager
 *   - Control task (TLS/TCP)
 *   - I2S capture task
 *   - Opus encoder task
 *   - RTP sender task
 *   - Health / watchdog monitor
 *
 * See ESP32_Audio_Device_Specification.md for full architecture.
 */

#include <stdio.h>
#include "esp_log.h"
#include "nvs_flash.h"

static const char *TAG = "app_main";

void app_main(void)
{
    ESP_LOGI(TAG, "ESP32-S3 Network Audio Device starting...");

    /* Initialise NVS — required for Wi-Fi and persistent config */
    esp_err_t ret = nvs_flash_init();
    if (ret == ESP_ERR_NVS_NO_FREE_PAGES ||
        ret == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        ESP_ERROR_CHECK(nvs_flash_erase());
        ret = nvs_flash_init();
    }
    ESP_ERROR_CHECK(ret);

    /*
     * TODO (P2/P3): Initialise subsystems and start tasks:
     *   1. nvs_config_init()       — load persistent configuration
     *   2. state_machine_init()    — set initial state to BOOT
     *   3. wifi_manager_init()     — start Wi-Fi provisioning/connection
     *   4. control_task_start()    — TLS/TCP control connection
     *   5. audio_buffer_init()     — PCM ring + encoded queue
     *   6. i2s_capture_start()     — I2S DMA capture task
     *   7. opus_encoder_start()    — Opus encoding task
     *   8. rtp_sender_start()      — RTP/UDP send task
     *   9. audio_manager_init()    — stream lifecycle coordination
     *  10. health_monitor_start()  — watchdog + diagnostics
     */

    ESP_LOGI(TAG, "Initialisation complete (stub — tasks not yet wired).");
}
