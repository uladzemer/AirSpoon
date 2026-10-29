package io.github.jqssun.airplay.audio

import java.nio.ByteBuffer
import java.nio.ByteOrder

/** Parses DMAP (iTunes metadata) binary TLV format into a tag->value map. */
object DmapParser {
    internal const val MAX_SIZE = 1024 * 1024
    internal const val MAX_DEPTH = 8
    internal const val MAX_ITEMS = 1024

    // tags whose payload is an integer (1/2/4/8 bytes)
    private val INT_TAGS = setOf(
        "astm", // duration ms
        "astn", // track number
        "asdk", // data kind
        "asts", // ???
        "miid", // item id
        "mcti", // container item id
        "mper", // persistent id
        "asai", // album id
        "asri", // artist id
        "asci", // composer id
        "asgi", // genre id
    )

    // tags that are containers (contain nested dmap items)
    private val CONTAINER_TAGS = setOf("mlit", "mcon", "mlcl")

    fun parse(data: ByteArray): Map<String, Any> {
        require(data.size <= MAX_SIZE) { "DMAP size limit" }
        return _parse(data, 0, data.size, 0, intArrayOf(0))
    }

    private fun _parse(data: ByteArray, start: Int, end: Int, depth: Int, items: IntArray): Map<String, Any> {
        require(depth <= MAX_DEPTH) { "DMAP depth limit" }
        val result = mutableMapOf<String, Any>()
        val buf = ByteBuffer.wrap(data, start, end - start).order(ByteOrder.BIG_ENDIAN)
        while (buf.remaining() >= 8) {
            require(++items[0] <= MAX_ITEMS) { "DMAP item limit" }
            val tag = String(data, buf.position(), 4, Charsets.US_ASCII)
            buf.position(buf.position() + 4)
            val len = buf.int
            if (len < 0 || len > buf.remaining()) break
            val offset = buf.position()
            result[tag] = when {
                tag in CONTAINER_TAGS -> _parse(data, offset, offset + len, depth + 1, items)
                tag in INT_TAGS -> _readInt(data, offset, offset + len)
                else -> String(data, offset, len, Charsets.UTF_8)
            }
            buf.position(offset + len)
        }
        return result
    }

    private fun _readInt(b: ByteArray, start: Int, end: Int): Long {
        var v = 0L
        for (i in start until end) v = (v shl 8) or (b[i].toLong() and 0xFF)
        return v
    }
}
