package io.github.jqssun.airplay.audio

import java.nio.ByteBuffer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class DmapParserTest {
    private fun item(tag: String, payload: ByteArray) =
        ByteBuffer.allocate(8 + payload.size).put(tag.toByteArray(Charsets.US_ASCII))
            .putInt(payload.size).put(payload).array()

    private fun nested(depth: Int): ByteArray {
        var data = item("minm", "Трек".toByteArray())
        repeat(depth) { data = item("mlit", data) }
        return data
    }

    @Test
    fun `контейнер не захватывает соседние элементы`() {
        val data = item("mlit", item("minm", "Трек".toByteArray())) +
            item("asar", "Автор".toByteArray()) + item("astm", byteArrayOf(0, 0, 1, 44))
        val map = DmapParser.parse(data)
        assertEquals(mapOf("minm" to "Трек"), map["mlit"])
        assertEquals("Автор", map["asar"])
        assertEquals(300L, map["astm"])
    }

    @Test
    fun `допускается ровно восемь уровней контейнеров`() {
        var map = DmapParser.parse(nested(DmapParser.MAX_DEPTH))
        repeat(DmapParser.MAX_DEPTH) {
            @Suppress("UNCHECKED_CAST")
            map = map["mlit"] as Map<String, Any>
        }
        assertEquals("Трек", map["minm"])
    }

    @Test(expected = IllegalArgumentException::class)
    fun `лишний уровень контейнера отклоняется`() {
        DmapParser.parse(nested(DmapParser.MAX_DEPTH + 1))
    }

    @Test(expected = IllegalArgumentException::class)
    fun `тысячи вложенных контейнеров не переполняют стек`() {
        DmapParser.parse(nested(2000))
    }

    @Test
    fun `размер на границе лимита допускается`() {
        val map = DmapParser.parse(item("minm", ByteArray(DmapParser.MAX_SIZE - 8) { 65 }))
        assertEquals(DmapParser.MAX_SIZE - 8, (map["minm"] as String).length)
    }

    @Test(expected = IllegalArgumentException::class)
    fun `слишком большой пакет отклоняется`() {
        DmapParser.parse(ByteArray(DmapParser.MAX_SIZE + 1))
    }

    @Test
    fun `число элементов на границе лимита допускается`() {
        val entry = item("minm", byteArrayOf())
        val data = ByteArray(entry.size * DmapParser.MAX_ITEMS) { entry[it % entry.size] }
        assertEquals(mapOf("minm" to ""), DmapParser.parse(data))
    }

    @Test(expected = IllegalArgumentException::class)
    fun `повторяющиеся теги тоже расходуют лимит элементов`() {
        val entry = item("minm", byteArrayOf())
        DmapParser.parse(ByteArray(entry.size * (DmapParser.MAX_ITEMS + 1)) { entry[it % entry.size] })
    }

    @Test(expected = IllegalArgumentException::class)
    fun `лимит элементов общий для соседних контейнеров`() {
        val entry = item("minm", byteArrayOf())
        val payload = ByteArray(entry.size * (DmapParser.MAX_ITEMS / 2)) { entry[it % entry.size] }
        DmapParser.parse(item("mlit", payload) + item("mcon", payload))
    }

    @Test
    fun `оборванные и переполненные длины не выходят за диапазон`() {
        for (length in listOf(-1, Int.MAX_VALUE, 1)) {
            val invalid = ByteBuffer.allocate(8).put("minm".toByteArray()).putInt(length).array()
            assertTrue(DmapParser.parse(invalid).isEmpty())
            val map = DmapParser.parse(item("mlit", invalid) + item("asar", "Автор".toByteArray()))
            assertEquals(emptyMap<String, Any>(), map["mlit"])
            assertEquals("Автор", map["asar"])
        }
        assertTrue(DmapParser.parse(ByteArray(7)).isEmpty())
        assertTrue(DmapParser.parse(byteArrayOf()).isEmpty())
    }
}
