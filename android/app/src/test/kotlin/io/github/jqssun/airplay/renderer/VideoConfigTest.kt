package io.github.jqssun.airplay.renderer

import org.junit.Assert.*
import org.junit.Test

class VideoConfigTest {
    private fun nal(type: Int, h265: Boolean = false, short: Boolean = false): ByteArray =
        (if (short) byteArrayOf(0, 0, 1) else byteArrayOf(0, 0, 0, 1)) +
            byteArrayOf((if (h265) type shl 1 else type).toByte(), 42)

    @Test fun `AVC parameters survive until an IDR arrives`() {
        val config = VideoConfig()
        val sps = nal(7)
        val pps = nal(8, short = true)
        assertFalse(config.observe(sps + pps, false))
        assertFalse(config.observe(nal(1), false))
        assertTrue(config.observe(nal(5, short = true), false))
        assertArrayEquals(sps, config.csd()[0])
        assertArrayEquals(nal(8), config.csd()[1])
    }

    @Test fun `HEVC requires VPS SPS PPS and detects random access frames`() {
        val config = VideoConfig()
        val parameters = nal(32, true) + nal(33, true, true) + nal(34, true)
        assertFalse(config.observe(parameters, true))
        assertArrayEquals(nal(32, true) + nal(33, true) + nal(34, true), config.csd().single())
        assertTrue(config.observe(nal(19, true), true))
        assertTrue(config.observe(nal(21, true), true))
        assertFalse(config.observe(nal(1, true), true))
    }

    @Test fun `session reset and codec switch do not reuse old parameters`() {
        val config = VideoConfig()
        config.observe(nal(7) + nal(8), false)
        config.clear()
        assertTrue(config.csd().isEmpty())
        config.observe(nal(7) + nal(8), false)
        config.observe(nal(19, true), true)
        assertTrue(config.csd().isEmpty())
        assertFalse(config.observe(byteArrayOf(0, 0, 1), true))
    }
}
