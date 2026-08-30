/*
 * test_util.h - tiny assert-based test helpers for the host test harness.
 */
#ifndef TEST_UTIL_H
#define TEST_UTIL_H

#include <stdio.h>
#include <stdlib.h>

static int g_checks = 0;
static int g_fails = 0;

#define CHECK(cond)                                                         \
    do {                                                                    \
        g_checks++;                                                         \
        if (!(cond)) {                                                      \
            g_fails++;                                                      \
            printf("  FAIL %s:%d: %s\n", __FILE__, __LINE__, #cond);        \
        }                                                                   \
    } while (0)

#define CHECK_EQ_INT(a, b)                                                  \
    do {                                                                    \
        g_checks++;                                                         \
        long _a = (long)(a), _b = (long)(b);                               \
        if (_a != _b) {                                                     \
            g_fails++;                                                      \
            printf("  FAIL %s:%d: %s (%ld) != %s (%ld)\n",                  \
                   __FILE__, __LINE__, #a, _a, #b, _b);                     \
        }                                                                   \
    } while (0)

#define TEST_MAIN_END(name)                                                 \
    do {                                                                    \
        if (g_fails == 0) {                                                 \
            printf("PASS %s (%d checks)\n", (name), g_checks);              \
            return 0;                                                       \
        }                                                                   \
        printf("FAILED %s (%d/%d checks failed)\n", (name), g_fails, g_checks); \
        return 1;                                                           \
    } while (0)

#endif /* TEST_UTIL_H */
