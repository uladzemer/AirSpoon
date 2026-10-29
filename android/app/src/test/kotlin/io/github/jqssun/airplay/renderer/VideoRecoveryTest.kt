package io.github.jqssun.airplay.renderer

import org.junit.Assert.*
import org.junit.Test

class VideoRecoveryTest {
    @Test fun `timeout retains the generation captured at the first failure`() {
        val recovery = VideoRecovery()
        recovery.codecFailed(0, 41L)
        recovery.codecFailed(2_000, 42L)
        assertEquals(41L, recovery.generationToDrop(3_000))
        assertEquals(0L, recovery.generationToDrop(3_001))
    }

    @Test fun `new session clears the failed generation and gets its own deadline`() {
        val recovery = VideoRecovery()
        recovery.codecFailed(0, 41L)
        recovery.cancel() // startSession, stopSession, or a recovered keyframe
        assertEquals(0L, recovery.generationToDrop(3_000))
        recovery.codecFailed(4_000, 42L)
        assertEquals(0L, recovery.generationToDrop(6_999))
        assertEquals(42L, recovery.generationToDrop(7_000))
    }

    @Test fun `unknown generation never drops or consumes the disconnect cooldown`() {
        val recovery = VideoRecovery()
        recovery.codecFailed(0, 0L)
        recovery.codecFailed(1_000, 42L) // cannot rebind an unknown failure to another session
        assertEquals(0L, recovery.generationToDrop(3_000))
        assertNull(recovery.nextCheckDelayMs(3_000))
        recovery.codecFailed(4_000, 42L)
        assertEquals(42L, recovery.generationToDrop(7_000))
    }

    @Test fun `without codec failure startup and dropped frames never trigger disconnect`() {
        val recovery = VideoRecovery()
        assertNull(recovery.nextCheckDelayMs(0))
        assertEquals(0L, recovery.generationToDrop(60_000))
    }

    @Test fun `failure times out after three seconds even without more frames`() {
        val recovery = VideoRecovery()
        recovery.codecFailed(0, 41L)
        assertEquals(3_000L, recovery.nextCheckDelayMs(0))
        assertEquals(0L, recovery.generationToDrop(2_999))
        assertEquals(41L, recovery.generationToDrop(3_000))
        assertEquals(0L, recovery.generationToDrop(60_000))
        assertNull(recovery.nextCheckDelayMs(60_000))
    }

    @Test fun `recovered keyframe or ended session cancels pending disconnect`() {
        val recovery = VideoRecovery()
        recovery.codecFailed(1_000, 41L)
        recovery.cancel()
        assertEquals(0L, recovery.generationToDrop(4_000))
        assertNull(recovery.nextCheckDelayMs(4_000))
        recovery.codecFailed(5_000, 41L)
        assertEquals(0L, recovery.generationToDrop(7_999))
        assertEquals(41L, recovery.generationToDrop(8_000))
    }

    @Test fun `repeated codec failures do not postpone the original deadline`() {
        val recovery = VideoRecovery()
        recovery.codecFailed(1_000, 41L)
        recovery.codecFailed(3_999, 41L)
        assertEquals(1L, recovery.nextCheckDelayMs(3_999))
        assertEquals(41L, recovery.generationToDrop(4_000))
    }

    @Test fun `disconnects are limited across reconnects and recovered keyframes`() {
        val recovery = VideoRecovery()
        recovery.codecFailed(0, 41L)
        assertEquals(41L, recovery.generationToDrop(3_000))
        recovery.cancel() // old session ends, a new one starts
        recovery.codecFailed(4_000, 41L)
        recovery.cancel() // keyframe recovers this failure
        recovery.codecFailed(5_000, 41L)
        assertEquals(0L, recovery.generationToDrop(8_000))
        assertEquals(5_000L, recovery.nextCheckDelayMs(8_000))
        assertEquals(0L, recovery.generationToDrop(12_999))
        assertEquals(41L, recovery.generationToDrop(13_000))
    }

    @Test fun `failure near end of cooldown still gets full keyframe timeout`() {
        val recovery = VideoRecovery()
        recovery.codecFailed(0, 41L)
        assertEquals(41L, recovery.generationToDrop(3_000))
        recovery.codecFailed(12_000, 41L)
        assertEquals(3_000L, recovery.nextCheckDelayMs(12_000))
        assertEquals(0L, recovery.generationToDrop(13_000))
        assertEquals(41L, recovery.generationToDrop(15_000))
    }

    @Test fun `late timer fires once with a large monotonic clock origin`() {
        val recovery = VideoRecovery()
        val origin = 9_000_000_000L
        recovery.codecFailed(origin, 41L)
        assertEquals(41L, recovery.generationToDrop(origin + 4_000))
        recovery.codecFailed(origin + 5_000, 41L)
        assertEquals(1L, recovery.nextCheckDelayMs(origin + 13_999))
        assertEquals(41L, recovery.generationToDrop(origin + 14_000))
    }
}
