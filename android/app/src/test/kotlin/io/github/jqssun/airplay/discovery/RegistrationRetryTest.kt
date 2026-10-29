package io.github.jqssun.airplay.discovery

import org.junit.Assert.assertEquals
import org.junit.Test

class RegistrationRetryTest {
    @Test fun `registration retries grow to one minute`() {
        assertEquals(listOf(2000L, 4000L, 8000L, 16000L, 32000L, 60000L, 60000L),
            (0..6).map(::registrationRetryMs))
    }

    @Test fun `attempt extremes cannot overflow`() {
        assertEquals(2000L, registrationRetryMs(Int.MIN_VALUE))
        assertEquals(60000L, registrationRetryMs(Int.MAX_VALUE))
    }
}
