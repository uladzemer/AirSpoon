package io.github.jqssun.airplay.discovery

internal fun registrationRetryMs(attempt: Int): Long =
    (2_000L shl attempt.coerceIn(0, 5)).coerceAtMost(60_000L)
