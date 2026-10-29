package io.github.jqssun.airplay.discovery

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.net.wifi.WifiManager
import android.os.Handler
import android.os.Looper
import android.util.Log

data class MdnsService(val name: String, val type: String, val port: Int, val txt: Map<String, String>)

class NsdServiceManager(private val ctx: Context) {

    private val nsdManager = ctx.getSystemService(Context.NSD_SERVICE) as NsdManager
    private var multicastLock: WifiManager.MulticastLock? = null
    private val handler = Handler(Looper.getMainLooper())
    private val registrations = mutableListOf<Registration>()

    private inner class Registration(val service: MdnsService) {
        var listener: NsdManager.RegistrationListener? = null
        var attempt = 0
        val retry = Runnable { if (this in registrations) _register(this) }
    }

    fun acquireMulticastLock() {
        val wifi = ctx.applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager
        multicastLock = wifi.createMulticastLock("airplay_mdns").apply {
            setReferenceCounted(false)
            acquire()
        }
    }

    fun register(services: List<MdnsService>) {
        unregisterAll()
        services.forEach { service ->
            val registration = Registration(service)
            registrations += registration
            _register(registration)
        }
    }

    private fun _register(registration: Registration) {
        val s = registration.service
        val info = NsdServiceInfo().apply {
            serviceName = s.name
            serviceType = s.type
            port = s.port
            s.txt.forEach { (k, v) -> setAttribute(k, v) }
        }
        val listener = object : NsdManager.RegistrationListener {
            override fun onServiceRegistered(info: NsdServiceInfo) {
                handler.post {
                    if (registration.listener !== this) {
                        try { nsdManager.unregisterService(this) } catch (_: Exception) {}
                        return@post
                    }
                    registration.attempt = 0
                    Log.i(TAG, "${s.type} registered: ${info.serviceName}")
                }
            }
            override fun onRegistrationFailed(info: NsdServiceInfo, code: Int) {
                Log.e(TAG, "${s.type} registration failed: $code")
                handler.post {
                    if (registration.listener === this) retry(registration)
                }
            }
            override fun onServiceUnregistered(info: NsdServiceInfo) {
                Log.i(TAG, "${s.type} unregistered")
            }
            override fun onUnregistrationFailed(info: NsdServiceInfo, code: Int) {
                Log.e(TAG, "${s.type} unregister failed: $code")
            }
        }
        registration.listener = listener
        try {
            nsdManager.registerService(info, NsdManager.PROTOCOL_DNS_SD, listener)
        } catch (e: Exception) {
            Log.w(TAG, "${s.type} registration error", e)
            retry(registration)
        }
    }

    private fun retire(registration: Registration) {
        val listener = registration.listener ?: return
        registration.listener = null
        try { nsdManager.unregisterService(listener) } catch (_: Exception) {}
    }

    private fun retry(registration: Registration) {
        retire(registration)
        if (registration !in registrations) return
        handler.removeCallbacks(registration.retry)
        val delayMs = registrationRetryMs(registration.attempt)
        registration.attempt = (registration.attempt + 1).coerceAtMost(6)
        handler.postDelayed(registration.retry, delayMs)
    }

    private fun unregisterAll() {
        registrations.forEach { registration ->
            handler.removeCallbacks(registration.retry)
            retire(registration)
        }
        registrations.clear()
    }

    fun release() {
        unregisterAll()
        multicastLock?.release()
        multicastLock = null
    }

    companion object {
        private const val TAG = "NsdServiceManager"
    }
}
