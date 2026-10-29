package io.github.jqssun.airplay.service

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.updateAndGet

// Return this disconnect's result; another callback may already have changed value.
internal fun MutableStateFlow<Int>.decrementConnectionCount(): Int =
    updateAndGet { (it - 1).coerceAtLeast(0) }
