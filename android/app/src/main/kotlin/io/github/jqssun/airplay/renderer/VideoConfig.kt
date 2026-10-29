package io.github.jqssun.airplay.renderer

internal class VideoConfig {
    private var h265 = false
    private val parameters = mutableMapOf<Int, ByteArray>()

    fun clear() = parameters.clear()

    fun observe(data: ByteArray, isH265: Boolean): Boolean {
        if (h265 != isH265) clear()
        h265 = isH265
        var keyframe = false
        var start = -1
        var header = -1
        var i = 0
        fun save(end: Int) {
            if (header < 0 || header >= end) return
            val type = if (h265) (data[header].toInt() shr 1) and 63 else data[header].toInt() and 31
            if (if (h265) type in 16..21 else type == 5) keyframe = true
            if ((if (h265) type in 32..34 else type == 7 || type == 8) && end - start <= 65536) {
                parameters[type] = byteArrayOf(0, 0, 0, 1) + data.copyOfRange(header, end)
            }
        }
        while (i + 2 < data.size) {
            val prefix = if (data[i] == 0.toByte() && data[i + 1] == 0.toByte()) {
                if (data[i + 2] == 1.toByte()) 3
                else if (i + 3 < data.size && data[i + 2] == 0.toByte() && data[i + 3] == 1.toByte()) 4
                else 0
            } else 0
            if (prefix > 0) {
                save(i)
                start = i
                header = i + prefix
                i += prefix
            } else i++
        }
        save(data.size)
        return keyframe
    }

    fun csd(): List<ByteArray> {
        return if (h265) {
            val parts = (32..34).map { parameters[it] ?: return emptyList() }
            listOf(parts.reduce { a, b -> a + b })
        } else {
            listOf(parameters[7] ?: return emptyList(), parameters[8] ?: return emptyList())
        }
    }
}
