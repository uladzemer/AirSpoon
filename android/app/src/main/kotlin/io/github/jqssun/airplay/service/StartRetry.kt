package io.github.jqssun.airplay.service

internal const val RETRY_BASE_MS = 5_000L
internal const val RETRY_MAX_MS = 60_000L

/**
 * Пауза перед повтором запуска сервера после сбоя (сеть есть, а сервер не поднялся): 5, 10, 20, 40 с,
 * дальше не больше 60 с. [attempt] считается с нуля (0 — пауза после первого сбоя).
 */
internal fun retryDelayMs(attempt: Int): Long =
    minOf(RETRY_BASE_MS shl attempt.coerceIn(0, 10), RETRY_MAX_MS)
