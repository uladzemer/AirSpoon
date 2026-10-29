package io.github.jqssun.airplay.renderer

// Accessed under VideoRenderer's lock; timestamps use a monotonic clock.
internal class VideoRecovery {
    private var failedAtMs: Long? = null
    private var lastDropAtMs: Long? = null
    private var failedGeneration = 0L

    fun codecFailed(nowMs: Long, generation: Long) {
        if (failedAtMs == null) {
            failedAtMs = nowMs
            failedGeneration = generation
        }
    }

    fun cancel() {
        failedAtMs = null
        failedGeneration = 0L
        // Keep the rate limit across recovered frames and session changes.
    }

    fun nextCheckDelayMs(nowMs: Long): Long? {
        val failedAt = failedAtMs ?: return null
        val cooldown = lastDropAtMs?.let { DROP_INTERVAL_MS - (nowMs - it) } ?: 0L
        return maxOf(0L, KEYFRAME_TIMEOUT_MS - (nowMs - failedAt), cooldown)
    }

    fun generationToDrop(nowMs: Long): Long {
        if (nextCheckDelayMs(nowMs) != 0L) return 0L
        val generation = failedGeneration
        cancel()
        if (generation == 0L) return 0L
        lastDropAtMs = nowMs
        return generation
    }

    companion object {
        private const val KEYFRAME_TIMEOUT_MS = 3_000L
        private const val DROP_INTERVAL_MS = 10_000L
    }
}
