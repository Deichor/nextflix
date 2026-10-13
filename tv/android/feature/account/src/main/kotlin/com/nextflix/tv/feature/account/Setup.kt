package com.nextflix.tv.feature.account

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.tv.material3.Text
import com.nextflix.tv.core.Core
import com.nextflix.tv.core.core
import com.nextflix.tv.core.nav.Navigator
import com.nextflix.tv.core.nav.Route
import com.nextflix.tv.core.session.ServerAddress
import com.nextflix.tv.core.ui.components.ButtonStyle
import com.nextflix.tv.core.ui.components.KeyboardField
import com.nextflix.tv.core.ui.components.KeyboardLayout
import com.nextflix.tv.core.ui.components.NfButton
import com.nextflix.tv.core.ui.components.OnScreenKeyboard
import com.nextflix.tv.core.ui.components.Wordmark
import com.nextflix.tv.core.ui.focus.FocusRestore
import com.nextflix.tv.core.ui.focus.restorableFocus
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

internal sealed interface SetupStatus {
    data object Idle : SetupStatus
    data object Testing : SetupStatus
    data class Ok(val version: String) : SetupStatus
    data class Failed(val error: Throwable) : SetupStatus
}

internal data class SetupState(val value: String, val status: SetupStatus = SetupStatus.Idle)

internal class SetupViewModel(private val core: Core) : ViewModel() {
    private val _state = MutableStateFlow(SetupState(core.session.server ?: ServerAddress.DEFAULT))
    val state: StateFlow<SetupState> = _state.asStateFlow()
    private var job: Job? = null

    init {
        // First run: probe the usual addresses and preselect the first one that answers
        if (core.session.server == null) {
            _state.update { it.copy(status = SetupStatus.Testing) }
            job = viewModelScope.launch {
                val found = ServerAddress.CANDIDATES.map { url ->
                    async { runCatching { url to core.api.checkServer(url, timeoutMs = 4_000) }.getOrNull() }
                }.awaitAll()
                val hit = found.firstOrNull { it != null }
                _state.value = if (hit != null) SetupState(hit.first, SetupStatus.Ok(hit.second)) else _state.value.copy(status = SetupStatus.Idle)
            }
        }
    }

    fun edit(value: String) {
        job?.cancel()
        _state.value = SetupState(value)
    }

    fun run(save: Boolean, onSaved: () -> Unit) {
        val url = ServerAddress.normalize(_state.value.value)
        job?.cancel()
        _state.value = SetupState(url, SetupStatus.Testing)
        job = viewModelScope.launch {
            try {
                val version = core.api.checkServer(url)
                _state.update { it.copy(status = SetupStatus.Ok(version)) }
                if (save) {
                    core.session.setServer(url)
                    onSaved()
                }
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                _state.update { it.copy(status = SetupStatus.Failed(e)) }
            }
        }
    }
}

/** First run, Settings > Server and Profiles > Change server (webOS screens/SetupScreen.tsx). */
@Composable
internal fun SetupContent(nav: Navigator) {
    val core = core()
    val vm = viewModel { SetupViewModel(core) }
    val state by vm.state.collectAsStateWithLifecycle()
    val s = accountStrings.current()

    Column(Modifier.fillMaxSize().padding(horizontal = 64.dp, vertical = 40.dp)) {
        Wordmark()
        Text(s.setupTitle, style = NfType.PageTitle, modifier = Modifier.padding(top = 16.dp))
        Row(Modifier.padding(top = 32.dp)) {
            Column(Modifier.width(400.dp)) {
                Text(s.setupAddress, style = NfType.Body.copy(fontSize = 12.sp), color = NfColors.Muted)
                Spacer(Modifier.padding(top = 6.dp))
                KeyboardField(state.value, Modifier.fillMaxWidth())
                Text(s.setupHint, style = NfType.BodySmall, color = NfColors.Dim, modifier = Modifier.padding(top = 10.dp))
                Text(s.setupLanHint, style = NfType.BodySmall, color = NfColors.Dim, modifier = Modifier.padding(top = 4.dp))
                Row(Modifier.padding(top = 26.dp), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                    NfButton(
                        s.setupSave,
                        onClick = { vm.run(save = true) { nav.reset(Route.Profiles) } },
                        modifier = Modifier.restorableFocus("setup-save"),
                    )
                    NfButton(
                        s.setupTest,
                        onClick = { vm.run(save = false) {} },
                        modifier = Modifier.restorableFocus("setup-test"),
                        style = ButtonStyle.Secondary,
                    )
                }
                val status = state.status
                Text(
                    when (status) {
                        SetupStatus.Idle -> ""
                        SetupStatus.Testing -> s.setupTesting
                        is SetupStatus.Ok -> s.setupOk(status.version)
                        is SetupStatus.Failed -> s.setupFailed(status.error.describe(s))
                    },
                    style = NfType.Body.copy(fontSize = 13.sp),
                    color = when (status) {
                        is SetupStatus.Ok -> OkGreen
                        is SetupStatus.Failed -> FailRed
                        else -> NfColors.TextSoft
                    },
                    modifier = Modifier.padding(top = 19.dp).heightIn(min = 32.dp),
                )
            }
            Spacer(Modifier.width(40.dp))
            OnScreenKeyboard(KeyboardLayout.Url, state.value, vm::edit, width = 380.dp)
        }
    }
    FocusRestore(ready = true, fallbackKey = "setup-save")
}
