package io.github.jqssun.airplay.ui

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.State
import androidx.compose.runtime.getValue
import androidx.compose.runtime.produceState
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.jqssun.airplay.R
import io.github.jqssun.airplay.localIpv4
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext

// Размеры шрифтов экрана ожидания для телевизора (видно с дивана).
// Требования: имя ≥ 64sp, текст ≥ 28sp.
private val TITLE_SIZE = 36.sp
private val HOWTO_SIZE = 30.sp
private val MESSAGE_SIZE = 40.sp
private val IP_SIZE = 28.sp

// Если по высоте остаётся меньше этого (например, 1080p при плотности 320 = 540dp минус нижнее
// меню), включается компактная раскладка: меньше отступы и имя, но шрифты не ниже требований.
// Прокрутить колонку пультом нельзя (в ней нет фокусируемых элементов), поэтому всё должно помещаться.
private val COMPACT_BELOW = 520.dp

/**
 * Размер шрифта имени: чем длиннее имя, тем меньше, но не меньше 64sp. Имя ограничено 20 знаками
 * (Prefs.SERVER_NAME_MAX_CHARS): 20 знаков по 64sp помещаются в одну строку при ширине ≥ 960dp.
 */
internal fun receiverNameFontSize(name: String, compact: Boolean = false) = when {
    name.length <= 12 -> if (compact) 80.sp else 96.sp
    name.length <= 16 -> if (compact) 72.sp else 80.sp
    else -> 64.sp
}

/**
 * Крупный экран ожидания для ТВ: «Выберите на телефоне или компьютере:», огромное имя приёмника,
 * инструкция по платформам и мелкий IP-адрес.
 *
 * Если [message] не пустой (приём выключен, ошибка, нужно разрешение), вместо имени и инструкции
 * показывается это сообщение крупным шрифтом.
 */
@Composable
fun TvWaitingContent(name: String, message: String?, modifier: Modifier = Modifier) {
    val ip by rememberLocalIpv4()
    // Box центрирует содержимое по вертикали; если оно не помещается, Column прокручивается
    BoxWithConstraints(modifier = modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        val compact = maxHeight < COMPACT_BELOW
        val titleSize = if (compact) 32.sp else TITLE_SIZE
        val howtoSize = if (compact) 28.sp else HOWTO_SIZE
        val gapAfterTitle = if (compact) 8.dp else 16.dp
        val gapAfterName = if (compact) 12.dp else 24.dp
        val gapBeforeIp = if (compact) 8.dp else 20.dp
        Column(
            modifier = Modifier
                .verticalScroll(rememberScrollState())
                .padding(horizontal = 48.dp, vertical = if (compact) 8.dp else 24.dp),
            horizontalAlignment = Alignment.CenterHorizontally
        ) {
            val textColor = MaterialTheme.colorScheme.onSurface
            val accent = MaterialTheme.colorScheme.primary
            if (message != null) {
                Text(
                    text = message,
                    fontSize = MESSAGE_SIZE,
                    lineHeight = MESSAGE_SIZE * 1.2f,
                    fontWeight = FontWeight.Medium,
                    color = textColor,
                    textAlign = TextAlign.Center
                )
            } else {
                Text(
                    text = stringResource(R.string.waiting_choose_title),
                    fontSize = titleSize,
                    fontWeight = FontWeight.Medium,
                    color = textColor,
                    textAlign = TextAlign.Center
                )
                Spacer(Modifier.height(gapAfterTitle))
                val nameSize = receiverNameFontSize(name, compact)
                Text(
                    text = name,
                    fontSize = nameSize,
                    lineHeight = nameSize * 1.1f,
                    fontWeight = FontWeight.Bold,
                    color = accent,
                    textAlign = TextAlign.Center,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis
                )
                Spacer(Modifier.height(gapAfterName))
                listOf(R.string.waiting_howto_ios, R.string.waiting_howto_mac, R.string.waiting_howto_windows)
                    .forEach { res ->
                        Text(
                            text = highlight(stringResource(res, name), name, accent),
                            fontSize = howtoSize,
                            lineHeight = howtoSize * (if (compact) 1.2f else 1.3f),
                            color = textColor,
                            textAlign = TextAlign.Center,
                            modifier = Modifier.padding(vertical = if (compact) 2.dp else 4.dp)
                        )
                    }
            }
            ip?.let {
                Spacer(Modifier.height(gapBeforeIp))
                Text(
                    text = stringResource(R.string.waiting_ip, it),
                    fontSize = IP_SIZE,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    textAlign = TextAlign.Center
                )
            }
        }
    }
}

/** Выделяет имя приёмника жирным акцентным цветом внутри строки инструкции. */
private fun highlight(text: String, name: String, color: Color): AnnotatedString {
    val at = if (name.isEmpty()) -1 else text.lastIndexOf(name)
    if (at < 0) return AnnotatedString(text)
    return buildAnnotatedString {
        append(text.substring(0, at))
        withStyle(SpanStyle(fontWeight = FontWeight.Bold, color = color)) { append(name) }
        append(text.substring(at + name.length))
    }
}

/** Локальный IPv4-адрес устройства; обновляется раз в 5 секунд (сеть может появиться после загрузки). */
@Composable
private fun rememberLocalIpv4(): State<String?> {
    val ctx = LocalContext.current.applicationContext
    return produceState<String?>(initialValue = null, ctx) {
        while (true) {
            value = withContext(Dispatchers.IO) { localIpv4(ctx) }
            delay(5_000)
        }
    }
}
