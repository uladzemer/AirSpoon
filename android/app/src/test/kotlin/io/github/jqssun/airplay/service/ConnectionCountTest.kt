package io.github.jqssun.airplay.service

import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class ConnectionCountTest {
    @Test fun `concurrent disconnects report the last connection exactly once`() {
        val executor = Executors.newFixedThreadPool(2)
        try {
            repeat(100) {
                val count = MutableStateFlow(2)
                val ready = CountDownLatch(2)
                val start = CountDownLatch(1)
                val bothUpdated = CountDownLatch(2)
                val results = (1..2).map {
                    executor.submit<Int> {
                        ready.countDown()
                        check(start.await(5, TimeUnit.SECONDS))
                        val remaining = count.decrementConnectionCount()
                        bothUpdated.countDown()
                        check(bothUpdated.await(5, TimeUnit.SECONDS))
                        // Both callbacks see value == 0 here, but only one returned 0.
                        assertEquals(0, count.value)
                        remaining
                    }
                }
                assertTrue(ready.await(5, TimeUnit.SECONDS))
                start.countDown()
                assertEquals(listOf(0, 1), results.map { it.get(5, TimeUnit.SECONDS) }.sorted())
            }
        } finally {
            executor.shutdownNow()
        }
    }

    @Test fun `count never becomes negative`() {
        val count = MutableStateFlow(1)
        assertEquals(0, count.decrementConnectionCount())
        assertEquals(0, count.decrementConnectionCount())
        assertEquals(0, count.value)
    }
}
