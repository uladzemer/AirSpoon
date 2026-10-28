package io.github.jqssun.airplay.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import io.github.jqssun.airplay.R
import io.github.jqssun.airplay.update.UpdateDialogState
import io.github.jqssun.airplay.viewmodel.MainViewModel

@Composable
fun UpdateDialog(state: UpdateDialogState, viewModel: MainViewModel) {
    val context = LocalContext.current

    when (state) {
        is UpdateDialogState.Offer -> {
            val updateFocus = remember { FocusRequester() }
            LaunchedEffect(Unit) { updateFocus.requestFocusUntilLanded() }

            AlertDialog(
                onDismissRequest = { viewModel.onUpdateLater() },
                title = { Text(stringResource(R.string.update_dialog_title)) },
                text = {
                    Text(stringResource(R.string.update_dialog_message, state.release.versionName))
                },
                confirmButton = {
                    Button(
                        onClick = { viewModel.onUpdateNow(context, state.release) },
                        modifier = Modifier
                            .focusRequester(updateFocus)
                            .dpadFocus()
                    ) {
                        Text(stringResource(R.string.btn_update))
                    }
                },
                dismissButton = {
                    TextButton(
                        onClick = { viewModel.onUpdateLater() },
                        modifier = Modifier.dpadFocus()
                    ) {
                        Text(stringResource(R.string.btn_later))
                    }
                }
            )
        }

        is UpdateDialogState.Downloading -> {
            val cancelFocus = remember { FocusRequester() }
            LaunchedEffect(Unit) { cancelFocus.requestFocusUntilLanded() }

            AlertDialog(
                onDismissRequest = { },
                title = { Text(stringResource(R.string.update_dialog_title)) },
                text = {
                    Column {
                        Text(stringResource(R.string.update_downloading, state.progress))
                        Spacer(Modifier.height(16.dp))
                        LinearProgressIndicator(
                            progress = { state.progress / 100f },
                            modifier = Modifier.fillMaxWidth()
                        )
                    }
                },
                confirmButton = {},
                dismissButton = {
                    TextButton(
                        onClick = { viewModel.onUpdateCancelDownload() },
                        modifier = Modifier
                            .focusRequester(cancelFocus)
                            .dpadFocus()
                    ) {
                        Text(stringResource(R.string.btn_cancel))
                    }
                }
            )
        }

        is UpdateDialogState.PermissionRequired -> {
            val settingsFocus = remember { FocusRequester() }
            LaunchedEffect(Unit) { settingsFocus.requestFocusUntilLanded() }

            AlertDialog(
                onDismissRequest = { viewModel.onUpdateLater() },
                title = { Text(stringResource(R.string.update_install_permission_title)) },
                text = { Text(stringResource(R.string.update_install_permission_desc)) },
                confirmButton = {
                    Button(
                        onClick = { viewModel.onUpdateOpenSettings(context) },
                        modifier = Modifier
                            .focusRequester(settingsFocus)
                            .dpadFocus()
                    ) {
                        Text(stringResource(R.string.btn_open_settings))
                    }
                },
                dismissButton = {
                    TextButton(
                        onClick = { viewModel.onUpdateLater() },
                        modifier = Modifier.dpadFocus()
                    ) {
                        Text(stringResource(R.string.btn_cancel))
                    }
                }
            )
        }

        is UpdateDialogState.Error -> {
            val okFocus = remember { FocusRequester() }
            LaunchedEffect(Unit) { okFocus.requestFocusUntilLanded() }

            AlertDialog(
                onDismissRequest = { viewModel.onUpdateDismissError() },
                title = { Text(stringResource(R.string.update_error_title)) },
                text = { Text(state.message) },
                confirmButton = {
                    Button(
                        onClick = { viewModel.onUpdateDismissError() },
                        modifier = Modifier
                            .focusRequester(okFocus)
                            .dpadFocus()
                    ) {
                        Text(stringResource(R.string.btn_ok))
                    }
                }
            )
        }
    }
}
