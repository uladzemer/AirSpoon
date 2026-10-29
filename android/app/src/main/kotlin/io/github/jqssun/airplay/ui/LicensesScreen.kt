package io.github.jqssun.airplay.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.focusable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.input.key.*
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import io.github.jqssun.airplay.BuildConfig
import io.github.jqssun.airplay.R
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

private data class LicensedComponent(val name: String, val license: String, val asset: String)

private val licensedComponents = listOf(
    LicensedComponent("AirSpoon Receiver / android-airplay-server", "GPL-3.0", "GPL-3.0.txt"),
    LicensedComponent("UxPlay", "GPL-3.0-or-later", "UxPlay-GPL-3.0.txt"),
    LicensedComponent("ShairPlay (UxPlay)", "LGPL-2.1-or-later", "LGPL-2.1.txt"),
    LicensedComponent("playfair", "GPL-3.0", "playfair-GPL-3.0.txt"),
    LicensedComponent("llhttp", "MIT", "llhttp-MIT.txt"),
    LicensedComponent("csrp (UxPlay)", "MIT", "csrp-MIT.txt"),
    LicensedComponent("libplist / libcnary", "LGPL-2.1-or-later", "LGPL-2.1.txt"),
    LicensedComponent("jsmn (libplist)", "MIT", "jsmn-MIT.txt"),
    LicensedComponent("time64 (libplist)", "MIT", "time64-MIT.txt"),
    LicensedComponent("OpenSSL", "Apache-2.0", "Apache-2.0.txt"),
    LicensedComponent("openssl-cmake", "MIT", "openssl-cmake-MIT.txt"),
    LicensedComponent("FFmpeg (ALAC, AAC)", "LGPL-2.1-or-later", "FFmpeg-LGPL-2.1.txt"),
    LicensedComponent("Oboe", "Apache-2.0", "Apache-2.0.txt"),
    LicensedComponent("AndroidX / Compose / Material / Media3", "Apache-2.0", "Apache-2.0.txt"),
    LicensedComponent("Dagger / Hilt", "Apache-2.0", "Apache-2.0.txt"),
    LicensedComponent("Kotlin / kotlinx.coroutines", "Apache-2.0", "Apache-2.0.txt"),
)

@Composable
fun LicensesScreen(onDismiss: () -> Unit) {
    var selected by rememberSaveable { mutableStateOf<Int?>(null) }
    var lastSelected by rememberSaveable { mutableIntStateOf(0) }
    val listState = rememberLazyListState()
    val back = { if (selected != null) selected = null else onDismiss() }
    // Отдельное окно удерживает фокус внутри экрана и возвращает его в настройки.
    Dialog(onDismissRequest = back, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Surface(Modifier.fillMaxSize()) {
            Column(Modifier.fillMaxSize().padding(24.dp)) {
                TextButton(onClick = back, modifier = Modifier.dpadFocus()) {
                    Text(stringResource(R.string.cd_back))
                }
                Text(stringResource(R.string.about_licenses), style = MaterialTheme.typography.headlineSmall)
                Spacer(Modifier.height(12.dp))
                val component = selected?.let { licensedComponents[it] }
                if (component == null) {
                    val firstFocus = remember { FocusRequester() }
                    LaunchedEffect(Unit) { firstFocus.requestFocusUntilLanded() }
                    LazyColumn(modifier = Modifier.weight(1f), state = listState) {
                        item {
                            Text(stringResource(R.string.about_version, BuildConfig.VERSION_NAME, BuildConfig.VERSION_CODE))
                            Text(stringResource(R.string.about_source))
                            Text(stringResource(R.string.about_license_hint))
                            Spacer(Modifier.height(12.dp))
                        }
                        itemsIndexed(licensedComponents) { index, entry ->
                            ListItem(
                                modifier = Modifier
                                    .thenIf(index == lastSelected) { focusRequester(firstFocus) }
                                    .dpadFocus(RectangleShape)
                                    .clickable { lastSelected = index; selected = index },
                                headlineContent = { Text(entry.name) },
                                supportingContent = { Text(entry.license) }
                            )
                        }
                    }
                } else {
                    key(component.asset) { LicenseText(component) }
                }
            }
        }
    }
}

@Composable
private fun ColumnScope.LicenseText(component: LicensedComponent) {
    val context = LocalContext.current
    val text by produceState<String?>(null, component.asset) {
        value = withContext(Dispatchers.IO) {
            context.assets.open("licenses/${component.asset}").bufferedReader().use { it.readText() }
        }
    }
    Text("${component.name} — ${component.license}", style = MaterialTheme.typography.titleMedium)
    val scroll = rememberScrollState()
    val scope = rememberCoroutineScope()
    val textFocus = remember { FocusRequester() }
    val content = text
    if (content != null) {
        LaunchedEffect(Unit) { textFocus.requestFocusUntilLanded() }
        // Фокус на всём тексте: пульт прокручивает даже длинные абзацы лицензии.
        Box(
            Modifier.weight(1f).fillMaxWidth()
                .focusRequester(textFocus)
                .dpadFocus(RectangleShape)
                .onPreviewKeyEvent { event ->
                    if (event.type != KeyEventType.KeyDown) return@onPreviewKeyEvent false
                    val delta = when (event.key) {
                        Key.DirectionDown, Key.PageDown -> scroll.viewportSize * 0.8f
                        Key.DirectionUp, Key.PageUp -> if (scroll.value > 0) -scroll.viewportSize * 0.8f else return@onPreviewKeyEvent false
                        else -> return@onPreviewKeyEvent false
                    }
                    scope.launch { scroll.scrollTo((scroll.value + delta.toInt()).coerceIn(0, scroll.maxValue)) }
                    true
                }
                .focusable()
                .verticalScroll(scroll)
                .padding(12.dp)
        ) {
            Text(content, style = MaterialTheme.typography.bodyMedium)
        }
    }
}
