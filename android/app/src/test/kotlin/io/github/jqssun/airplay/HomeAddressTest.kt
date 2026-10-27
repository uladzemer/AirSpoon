package io.github.jqssun.airplay

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** Проверка выбора адреса для экрана ожидания: домашняя сеть важнее VPN. */
class HomeAddressTest {
    private fun a(iface: String, ip: String) = IfaceAddress(iface, ip)

    @Test
    fun `домашний Wi-Fi выбирается раньше tailscale, даже если tailscale идёт первым`() {
        val list = listOf(a("tailscale0", "100.101.102.103"), a("wlan0", "192.168.1.50"))
        assertEquals("192.168.1.50", pickHomeAddress(list))
    }

    @Test
    fun `кабель eth0 с адресом 10_x считается домашним`() {
        val list = listOf(a("tun0", "10.8.0.2"), a("eth0", "10.0.0.7"))
        assertEquals("10.0.0.7", pickHomeAddress(list))
    }

    @Test
    fun `частный адрес 172_16-31 домашний, а 172_32 нет`() {
        assertTrue(isPrivateLan("172.16.0.1"))
        assertTrue(isPrivateLan("172.31.255.254"))
        assertFalse(isPrivateLan("172.32.0.1"))
        assertFalse(isPrivateLan("172.15.0.1"))
    }

    @Test
    fun `адрес CGNAT на wlan0 считается пригодным, на мобильном интерфейсе нет`() {
        assertTrue(hasUsableAddress(listOf(a("wlan0", "100.100.1.5"))))
        assertFalse(hasUsableAddress(listOf(a("rmnet0", "100.100.1.5"))))
    }

    @Test
    fun `link-local, петля и VPN не дают пригодной сети`() {
        assertFalse(hasUsableAddress(listOf(a("wlan0", "169.254.10.20"))))
        assertFalse(hasUsableAddress(listOf(a("lo", "127.0.0.1"))))
        assertFalse(hasUsableAddress(listOf(a("tailscale0", "100.101.102.103"), a("tun0", "10.8.0.2"))))
        assertTrue(hasUsableAddress(listOf(a("tun0", "10.8.0.2"), a("eth0", "192.168.0.9"))))
    }

    @Test
    fun `CGNAT 100_64-127 распознаётся`() {
        assertTrue(isCgnat("100.64.0.1"))
        assertTrue(isCgnat("100.127.255.255"))
        assertFalse(isCgnat("100.128.0.1"))
        assertFalse(isCgnat("100.63.0.1"))
    }

    @Test
    fun `VPN-интерфейсы распознаются`() {
        listOf("tailscale0", "tun0", "ppp0", "wg0", "utun3").forEach { assertTrue(it, isVpnInterface(it)) }
        listOf("wlan0", "eth0", "rmnet0").forEach { assertFalse(it, isVpnInterface(it)) }
    }

    @Test
    fun `адрес 100_64 на wlan не считается домашним, берётся настоящий частный`() {
        val list = listOf(a("wlan0", "100.100.1.1"), a("eth0", "192.168.0.9"))
        assertEquals("192.168.0.9", pickHomeAddress(list))
    }

    @Test
    fun `если домашнего нет, берётся не-VPN адрес`() {
        val list = listOf(a("tailscale0", "100.90.0.1"), a("rmnet0", "203.0.113.5"))
        assertEquals("203.0.113.5", pickHomeAddress(list))
    }

    @Test
    fun `если есть только VPN, показывается он`() {
        val list = listOf(a("tailscale0", "100.90.0.1"))
        assertEquals("100.90.0.1", pickHomeAddress(list))
    }

    @Test
    fun `петля и link-local игнорируются, пустой список даёт null`() {
        assertNull(pickHomeAddress(emptyList()))
        assertNull(pickHomeAddress(listOf(a("lo", "127.0.0.1"), a("wlan0", "169.254.3.4"))))
    }

    @Test
    fun `мусорный адрес не ломает выбор`() {
        val list = listOf(a("wlan0", "fe80::1"), a("wlan0", "300.1.1.1"), a("wlan1", "192.168.5.5"))
        assertEquals("192.168.5.5", pickHomeAddress(list))
    }
}
