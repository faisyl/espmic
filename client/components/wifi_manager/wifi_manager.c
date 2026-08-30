/*
 * wifi_manager.c - Wi-Fi station + provisioning (spec Sections 11, 12, 13, 16).
 */
#include "wifi_manager.h"

#include <string.h>
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "esp_wifi.h"
#include "esp_event.h"
#include "esp_netif.h"
#include "esp_log.h"

#include "wifi_provisioning/manager.h"
#include "wifi_provisioning/scheme_softap.h"

static const char *TAG = "wifi_mgr";

/* Bounded auto-reconnect backoff bookkeeping. */
#define WIFI_RECONNECT_DELAY_MS 2000

static struct {
    bool                    inited;
    wifi_manager_config_t   cfg;
    esp_netif_t            *sta_netif;
    volatile bool           connected;
    volatile bool           provisioning;
} g;

static void notify_got_ip(uint32_t ip)
{
    if (g.cfg.on_got_ip) g.cfg.on_got_ip(ip, g.cfg.user);
}
static void notify_disc(void)
{
    if (g.cfg.on_disconnected) g.cfg.on_disconnected(g.cfg.user);
}

/* Unified event handler for WIFI_EVENT, IP_EVENT and WIFI_PROV_EVENT. */
static void event_handler(void *arg, esp_event_base_t base,
                          int32_t id, void *data)
{
    if (base == WIFI_PROV_EVENT) {
        switch (id) {
        case WIFI_PROV_START:
            g.provisioning = true;
            ESP_LOGI(TAG, "provisioning started");
            if (g.cfg.on_provisioning_started) g.cfg.on_provisioning_started(g.cfg.user);
            break;
        case WIFI_PROV_CRED_SUCCESS:
            ESP_LOGI(TAG, "provisioning credentials accepted");
            if (g.cfg.on_provisioned) g.cfg.on_provisioned(g.cfg.user);
            break;
        case WIFI_PROV_END:
            /* Stop the provisioning service after successful setup (spec 13). */
            wifi_prov_mgr_deinit();
            g.provisioning = false;
            ESP_LOGI(TAG, "provisioning ended");
            break;
        default:
            break;
        }
        return;
    }

    if (base == WIFI_EVENT) {
        switch (id) {
        case WIFI_EVENT_STA_START:
            esp_wifi_connect();
            break;
        case WIFI_EVENT_STA_DISCONNECTED:
            g.connected = false;
            notify_disc();
            /* Auto-reconnect (spec Section 14). */
            vTaskDelay(pdMS_TO_TICKS(WIFI_RECONNECT_DELAY_MS));
            esp_wifi_connect();
            break;
        default:
            break;
        }
        return;
    }

    if (base == IP_EVENT && id == IP_EVENT_STA_GOT_IP) {
        ip_event_got_ip_t *evt = (ip_event_got_ip_t *)data;
        g.connected = true;
        ESP_LOGI(TAG, "got ip: " IPSTR, IP2STR(&evt->ip_info.ip));
        notify_got_ip(evt->ip_info.ip.addr);
    }
}

esp_err_t wifi_manager_init(const wifi_manager_config_t *cfg)
{
    if (!cfg) return ESP_ERR_INVALID_ARG;
    if (g.inited) return ESP_OK;
    g.cfg = *cfg;

    ESP_ERROR_CHECK(esp_netif_init());
    ESP_ERROR_CHECK(esp_event_loop_create_default());
    g.sta_netif = esp_netif_create_default_wifi_sta();

    wifi_init_config_t wcfg = WIFI_INIT_CONFIG_DEFAULT();
    ESP_ERROR_CHECK(esp_wifi_init(&wcfg));

    ESP_ERROR_CHECK(esp_event_handler_register(WIFI_EVENT, ESP_EVENT_ANY_ID,
                                               event_handler, NULL));
    ESP_ERROR_CHECK(esp_event_handler_register(IP_EVENT, IP_EVENT_STA_GOT_IP,
                                               event_handler, NULL));
    ESP_ERROR_CHECK(esp_event_handler_register(WIFI_PROV_EVENT, ESP_EVENT_ANY_ID,
                                               event_handler, NULL));

    ESP_ERROR_CHECK(esp_wifi_set_storage(WIFI_STORAGE_FLASH));
    ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_STA));

    g.inited = true;
    ESP_LOGI(TAG, "initialised");
    return ESP_OK;
}

static esp_err_t start_provisioning(void)
{
    wifi_prov_mgr_config_t pcfg = {
        .scheme = wifi_prov_scheme_softap,
        .scheme_event_handler = WIFI_PROV_EVENT_HANDLER_NONE,
    };
    ESP_ERROR_CHECK(wifi_prov_mgr_init(pcfg));

    const char *service = g.cfg.service_name[0] ? g.cfg.service_name : "PROV_ESP32";
    const char *pop = g.cfg.pop[0] ? g.cfg.pop : NULL;
    wifi_prov_security_t sec = pop ? WIFI_PROV_SECURITY_1 : WIFI_PROV_SECURITY_0;

    ESP_LOGI(TAG, "starting provisioning as '%s'", service);
    return wifi_prov_mgr_start_provisioning(sec, pop, service, NULL);
}

esp_err_t wifi_manager_start(void)
{
    if (!g.inited) return ESP_ERR_INVALID_STATE;

    bool provisioned = false;
    /* wifi_prov_mgr must be temporarily inited to query provisioning state. */
    wifi_prov_mgr_config_t pcfg = {
        .scheme = wifi_prov_scheme_softap,
        .scheme_event_handler = WIFI_PROV_EVENT_HANDLER_NONE,
    };
    ESP_ERROR_CHECK(wifi_prov_mgr_init(pcfg));
    ESP_ERROR_CHECK(wifi_prov_mgr_is_provisioned(&provisioned));

    if (!provisioned) {
        const char *service = g.cfg.service_name[0] ? g.cfg.service_name : "PROV_ESP32";
        const char *pop = g.cfg.pop[0] ? g.cfg.pop : NULL;
        wifi_prov_security_t sec = pop ? WIFI_PROV_SECURITY_1 : WIFI_PROV_SECURITY_0;
        ESP_LOGI(TAG, "no credentials; entering provisioning '%s'", service);
        return wifi_prov_mgr_start_provisioning(sec, pop, service, NULL);
    }

    /* Provisioned: release the prov manager and start as a normal station. */
    wifi_prov_mgr_deinit();
    ESP_LOGI(TAG, "credentials present; starting station");
    return esp_wifi_start();
}

esp_err_t wifi_manager_reset_credentials(void)
{
    ESP_LOGW(TAG, "resetting Wi-Fi credentials");
    esp_wifi_disconnect();
    esp_wifi_stop();
    /* Clear stored station config and re-enter provisioning. */
    esp_err_t err = esp_wifi_restore();
    if (err != ESP_OK) ESP_LOGW(TAG, "wifi_restore: %s", esp_err_to_name(err));
    ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_STA));
    return start_provisioning();
}

bool wifi_manager_is_connected(void)
{
    return g.connected;
}
