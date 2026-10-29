package io.github.jqssun.airplay.update

import android.util.Base64
import io.github.jqssun.airplay.bridge.NativeBridge

object UpdateSignature {
    // Base64 от 32 байт Ed25519. Заменить перед первым релизом.
    const val PUBLIC_KEY = "QDIpKAuvK0T7meBr/55aHsuDpFPmUMQhBLPEZ/UhjHQ="

    fun verify(data: ByteArray, signature: ByteArray, version: String): UpdateManifest {
        try {
            val key = Base64.decode(PUBLIC_KEY, Base64.DEFAULT)
            if (key.size != 32) throw SecurityException("Ключ проверки обновлений не настроен")
            val sig = Base64.decode(signature, Base64.DEFAULT)
            return verifyUpdateManifest(data, sig, key, version) { publicKey, message, signed ->
                NativeBridge.nativeVerifyEd25519(publicKey, message, signed)
            }
        } catch (e: IllegalArgumentException) {
            throw SecurityException("Недопустимый ключ или подпись манифеста обновления", e)
        } catch (e: LinkageError) {
            throw SecurityException("Не удалось запустить проверку подписи обновления", e)
        }
    }
}
