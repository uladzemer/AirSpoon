package io.github.jqssun.airplay.service

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** Пауза перед повтором запуска сервера: растёт вдвое и упирается в потолок. */
class StartRetryTest {
    @Test
    fun `паузы растут вдвое начиная с 5 секунд`() {
        assertEquals(5_000L, retryDelayMs(0))
        assertEquals(10_000L, retryDelayMs(1))
        assertEquals(20_000L, retryDelayMs(2))
        assertEquals(40_000L, retryDelayMs(3))
    }

    @Test
    fun `пауза не больше минуты, сколько бы ни было неудач`() {
        assertEquals(60_000L, retryDelayMs(4))
        assertEquals(60_000L, retryDelayMs(10))
        assertEquals(60_000L, retryDelayMs(1_000))
    }

    @Test
    fun `отрицательный номер попытки не ломает расчёт`() {
        assertEquals(5_000L, retryDelayMs(-3))
        assertTrue(retryDelayMs(Int.MAX_VALUE) <= 60_000L)
    }
}
