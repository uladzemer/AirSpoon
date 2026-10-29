package io.github.jqssun.airplay.update

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class UpdateBusyTest {
    @Test fun `audio without video and connections without metadata both block installation`() {
        assertTrue(updateBusy(false, false, false, true, 0))
        assertTrue(updateBusy(false, false, false, false, 1))
    }

    @Test fun `all video modes block installation`() {
        assertTrue(updateBusy(true, false, false, false, 0))
        assertTrue(updateBusy(false, true, false, false, 0))
        assertTrue(updateBusy(false, false, true, false, 0))
    }

    @Test fun `busy is evaluated again when session changes`() {
        var audio = false
        val canCommit = { !updateBusy(false, false, false, audio, 0) }
        assertTrue(canCommit())
        audio = true
        assertFalse(canCommit())
        audio = false
        assertTrue(canCommit())
    }
}
