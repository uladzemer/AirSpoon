package io.github.jqssun.airplay

import android.content.Context
import android.net.ConnectivityManager
import java.net.Inet4Address
import java.net.NetworkInterface

/** IPv4-адрес с именем сетевого интерфейса, на котором он висит. */
internal data class IfaceAddress(val iface: String, val address: String)

// Интерфейсы VPN и туннелей: адрес на них не виден телефонам в домашней сети.
private val VPN_PREFIXES = listOf("tailscale", "tun", "ppp", "wg", "utun", "ipsec", "zt", "vpn")
// Интерфейсы домашней сети: Wi-Fi и кабель.
private val HOME_PREFIXES = listOf("wlan", "eth")

private fun parseIpv4(address: String): IntArray? {
    val parts = address.split('.')
    if (parts.size != 4) return null
    val octets = IntArray(4)
    for (i in 0 until 4) {
        octets[i] = parts[i].toIntOrNull()?.takeIf { it in 0..255 } ?: return null
    }
    return octets
}

/** Частный адрес по RFC 1918: 192.168/16, 10/8, 172.16/12. */
internal fun isPrivateLan(address: String): Boolean {
    val o = parseIpv4(address) ?: return false
    return o[0] == 10 || (o[0] == 192 && o[1] == 168) || (o[0] == 172 && o[1] in 16..31)
}

/** Общий адрес оператора (CGNAT) 100.64.0.0/10: так выглядит Tailscale и часть мобильных сетей. */
internal fun isCgnat(address: String): Boolean {
    val o = parseIpv4(address) ?: return false
    return o[0] == 100 && o[1] in 64..127
}

internal fun isVpnInterface(name: String): Boolean {
    val n = name.lowercase()
    return VPN_PREFIXES.any { n.startsWith(it) }
}

/**
 * Выбирает адрес для показа на экране ожидания: сначала домашняя сеть (wlan*, eth* с частным адресом),
 * затем любой адрес не из VPN (не tailscale/tun/ppp и не 100.64.0.0/10), затем что угодно.
 * Петлевые (127.*) и link-local (169.254.*) адреса не берутся вовсе.
 */
internal fun pickHomeAddress(candidates: List<IfaceAddress>): String? {
    val usable = candidates.filter { c ->
        val o = parseIpv4(c.address)
        o != null && o[0] != 127 && o[0] != 0 && !(o[0] == 169 && o[1] == 254)
    }
    val home = usable.filter { c ->
        val n = c.iface.lowercase()
        HOME_PREFIXES.any { n.startsWith(it) } && !isVpnInterface(n) && isPrivateLan(c.address) && !isCgnat(c.address)
    }
    // Wi-Fi и кабель равноправны; при двух сразу берём первый в порядке перечисления
    home.firstOrNull()?.let { return it.address }
    val nonVpn = usable.filter { !isVpnInterface(it.iface) && !isCgnat(it.address) }
    nonVpn.firstOrNull()?.let { return it.address }
    return usable.firstOrNull()?.address
}

/** Собирает IPv4-адреса всех поднятых интерфейсов (через ConnectivityManager и NetworkInterface). */
internal fun collectIpv4Addresses(context: Context): List<IfaceAddress> {
    val result = LinkedHashSet<IfaceAddress>()
    runCatching {
        val cm = context.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        for (network in cm.allNetworks) {
            val lp = cm.getLinkProperties(network) ?: continue
            val name = lp.interfaceName ?: continue
            for (la in lp.linkAddresses) {
                val a = la.address
                if (a is Inet4Address) a.hostAddress?.let { result.add(IfaceAddress(name, it)) }
            }
        }
    }
    runCatching {
        for (ni in NetworkInterface.getNetworkInterfaces().toList()) {
            if (!ni.isUp || ni.isLoopback) continue
            for (a in ni.inetAddresses.toList()) {
                if (a is Inet4Address) a.hostAddress?.let { result.add(IfaceAddress(ni.name, it)) }
            }
        }
    }
    return result.toList()
}

/** IPv4-адрес домашней сети для показа пользователю; null, если сети нет. */
internal fun localIpv4(context: Context): String? = pickHomeAddress(collectIpv4Addresses(context))

/**
 * Есть ли среди адресов такой, на который телефоны и компьютеры могут подключиться: не VPN,
 * не link-local (169.254.*) и не петлевой. Адрес из 100.64/10 на Wi-Fi или кабеле (роутер или
 * провайдер с CGNAT прямо в локальной сети) тоже подходит, как и в pickHomeAddress;
 * на прочих интерфейсах такой адрес считается общим адресом оператора и не подходит.
 */
internal fun hasUsableAddress(candidates: List<IfaceAddress>): Boolean = candidates.any {
    val home = HOME_PREFIXES.any { p -> it.iface.lowercase().startsWith(p) }
    !isVpnInterface(it.iface) && (home || !isCgnat(it.address)) && pickHomeAddress(listOf(it)) != null
}

/** Есть ли сейчас сетевой адрес, на который можно подключиться (см. hasUsableAddress). */
internal fun hasUsableNetwork(context: Context): Boolean = hasUsableAddress(collectIpv4Addresses(context))
