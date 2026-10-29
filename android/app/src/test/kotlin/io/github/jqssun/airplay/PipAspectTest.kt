package io.github.jqssun.airplay

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class PipAspectTest {
    @Test fun `portrait and landscape ratios stay in the PiP range`() {
        for (aspect in listOf(0f, -1f, 0.1f, 9f / 16, 1f, 16f / 9, 3f, Float.MAX_VALUE,
            Float.NaN, Float.POSITIVE_INFINITY, Float.NEGATIVE_INFINITY)) {
            val ratio = pipAspectNumerator(aspect) / 1000.0
            assertTrue(ratio >= 1.0 / 2.39 && ratio <= 2.39)
        }
        assertEquals(419, pipAspectNumerator(0f))
        assertEquals(2390, pipAspectNumerator(4f))
    }

    @Test fun `valid aspect is retained and NaN uses default`() {
        assertEquals(1000, pipAspectNumerator(1f))
        assertEquals(562, pipAspectNumerator(9f / 16))
        assertEquals(pipAspectNumerator(16f / 9), pipAspectNumerator(Float.NaN))
    }
}
