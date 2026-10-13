package com.nextflix.tv.feature.account

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.tv.material3.Text
import com.nextflix.tv.core.core
import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.i18n.Formats
import com.nextflix.tv.core.i18n.LocalAppLanguage
import com.nextflix.tv.core.i18n.strings
import com.nextflix.tv.core.ui.components.ButtonStyle
import com.nextflix.tv.core.ui.components.KeyboardField
import com.nextflix.tv.core.ui.components.KeyboardLayout
import com.nextflix.tv.core.ui.components.NfButton
import com.nextflix.tv.core.ui.components.OnScreenKeyboard
import com.nextflix.tv.core.ui.components.Spinner
import com.nextflix.tv.core.ui.focus.tryFocus
import com.nextflix.tv.core.ui.theme.Dimens
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType

private enum class Field { Url, Key }

/** Indexer (Jackett) settings (web IndexerCard): URL, API key (write-only), Bitsearch; Save and Test. */
@Composable
internal fun IndexerPanel(onClose: () -> Unit) {
    val core = core()
    val vm = viewModel(key = "indexer") { IndexerViewModel(ServerSettingsApi(core.api)) { it.describe(accountStrings[core.session.language]) } }
    val state by vm.state.collectAsStateWithLifecycle()
    val s = accountStrings.current()
    val common = strings()
    var editing by rememberSaveable { mutableStateOf<Field?>(null) }
    var lastField by rememberSaveable { mutableStateOf(Field.Url) }
    val urlRow = remember { FocusRequester() }
    val keyRow = remember { FocusRequester() }
    LaunchedEffect(Unit) { vm.load() }
    fun edit(field: Field) {
        lastField = field
        editing = field
    }

    when (editing) {
        Field.Url -> TextEditor(
            label = s.jackettUrl,
            initial = state.url,
            secret = false,
            onDone = { vm.setUrl(it); editing = null },
            onCancel = { editing = null },
        )
        Field.Key -> TextEditor(
            label = s.apiKey,
            hint = s.indexerKeyHint,
            initial = state.key,
            secret = true,
            stored = state.loaded?.apiKeySet == true && !state.keyCleared,
            onDone = { vm.setKey(it); editing = null },
            onRemove = { vm.clearKey(); editing = null },
            onCancel = { editing = null },
        )
        null -> PanelFrame(
            title = s.indexerTitle,
            summary = s.indexerSummary,
            connection = state.connection,
            connectionNote = state.connectionError,
            loading = state.loaded == null && state.loadError == null,
            loadError = state.loadError?.let { it.ifEmpty { s.couldNotLoad } },
            onRetry = vm::load,
            notice = noticeText(state.notice, s) { r -> r.success to (if (r.success) r.message.ifEmpty { s.jackettConnected } else r.message) },
            initialFocus = if (lastField == Field.Key) keyRow else urlRow,
        ) {
            SettingRow(
                s.jackettUrl,
                onClick = { edit(Field.Url) },
                value = state.url.ifEmpty { s.keyNotSet },
                modifier = Modifier.focusRequester(urlRow),
            )
            KeyRow(s, s.indexerKeyHint, state.loaded?.apiKeySet == true, state.key, state.keyCleared, Modifier.focusRequester(keyRow)) {
                edit(Field.Key)
            }
            SettingRow(s.bitsearch, onClick = vm::toggleBitsearch, hint = s.bitsearchHint, value = if (state.bitsearch) common.on else common.off)
            Buttons(
                saveLabel = if (state.saving) common.saving else common.save,
                canSave = state.dirty && !state.saving,
                onSave = vm::save,
                testLabel = if (state.testing) s.statusChecking else s.testConnection,
                canTest = !state.testing && state.url.isNotBlank(),
                onTest = vm::test,
            )
        }
    }
    BackHandler(enabled = editing == null, onBack = onClose)
}

/** TorBox settings (web DebridCard): API key (write-only) and three switches; Save and Test with the plan. */
@Composable
internal fun DebridPanel(onClose: () -> Unit) {
    val core = core()
    val vm = viewModel(key = "debrid") { DebridViewModel(ServerSettingsApi(core.api)) { it.describe(accountStrings[core.session.language]) } }
    val state by vm.state.collectAsStateWithLifecycle()
    val s = accountStrings.current()
    val common = strings()
    val language = LocalAppLanguage.current
    var editing by rememberSaveable { mutableStateOf(false) }
    val keyRow = remember { FocusRequester() }
    LaunchedEffect(Unit) { vm.load() }
    val onOff = { on: Boolean -> if (on) common.on else common.off }

    if (editing) {
        TextEditor(
            label = s.apiKey,
            hint = s.debridKeyHint,
            initial = state.key,
            secret = true,
            stored = state.loaded?.apiKeySet == true && !state.keyCleared,
            onDone = { vm.setKey(it); editing = false },
            onRemove = { vm.clearKey(); editing = false },
            onCancel = { editing = false },
        )
    } else {
        PanelFrame(
            title = s.debridTitle,
            summary = s.debridSummary,
            connection = state.connection,
            connectionNote = state.connectionResult?.let { if (it.success) planText(it, s, language) else it.message }.orEmpty(),
            loading = state.loaded == null && state.loadError == null,
            loadError = state.loadError?.let { it.ifEmpty { s.couldNotLoad } },
            onRetry = vm::load,
            notice = noticeText(state.notice, s) { r ->
                (r.success && r.active != false) to (if (r.success) planText(r, s, language) else r.message)
            },
            initialFocus = keyRow,
        ) {
            KeyRow(s, s.debridKeyHint, state.loaded?.apiKeySet == true, state.key, state.keyCleared, Modifier.focusRequester(keyRow)) {
                editing = true
            }
            SettingRow(s.debridEnabled, onClick = vm::toggleEnabled, hint = s.debridEnabledHint, value = onOff(state.enabled))
            SettingRow(s.debridPrefer, onClick = vm::togglePrefer, hint = s.debridPreferHint, value = onOff(state.prefer))
            SettingRow(s.debridAddUncached, onClick = vm::toggleAddUncached, hint = s.debridAddUncachedHint, value = onOff(state.addUncached))
            Buttons(
                saveLabel = if (state.saving) common.saving else common.save,
                canSave = state.dirty && !state.saving,
                onSave = vm::save,
                testLabel = if (state.testing) s.statusChecking else s.testConnection,
                canTest = state.canTest,
                onTest = vm::test,
            )
        }
    }
    BackHandler(enabled = !editing, onBack = onClose)
}

private fun planText(r: TestResult, s: AccountStrings, language: AppLanguage): String {
    val plan = r.planName?.ifEmpty { null } ?: "TorBox"
    val date = r.expiresAt?.let { Formats.date(language, it) }.orEmpty()
    return when {
        r.active == false -> s.debridPlanExpired(plan)
        date.isEmpty() -> s.debridPlanNoDate(plan)
        else -> s.debridPlan(plan, date)
    }
}

/** (ok, text) of the line under the buttons. */
private fun noticeText(notice: Notice?, s: AccountStrings, tested: (TestResult) -> Pair<Boolean, String>): Pair<Boolean, String>? =
    when (notice) {
        null -> null
        Notice.Saved -> true to s.saved
        is Notice.SaveFailed -> false to notice.message.ifEmpty { s.couldNotSave }
        is Notice.Tested -> tested(notice.result)
        is Notice.TestFailed -> false to notice.message.ifEmpty { s.couldNotReachServer }
    }

@Composable
private fun PanelFrame(
    title: String,
    summary: String,
    connection: Connection,
    connectionNote: String,
    loading: Boolean,
    loadError: String?,
    onRetry: () -> Unit,
    notice: Pair<Boolean, String>?,
    initialFocus: FocusRequester,
    rows: @Composable () -> Unit,
) {
    val s = accountStrings.current()
    val retry = remember { FocusRequester() }
    Column(
        Modifier
            .fillMaxHeight()
            .padding(start = Dimens.ScreenPadH)
            .verticalScroll(rememberScrollState())
            .padding(top = 56.dp, bottom = 40.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Text(title, style = NfType.PageTitle.copy(fontSize = 29.sp))
        Text(summary, style = NfType.Body.copy(fontSize = 13.sp), color = NfColors.Muted, modifier = Modifier.width(SettingWidth))
        val (badge, color) = when (connection) {
            Connection.Checking -> s.statusChecking to NfColors.Muted
            Connection.Connected -> s.statusConnected to OkGreen
            Connection.NotSet -> s.statusNotSet to NfColors.Dim
            Connection.Error -> s.statusError to FailRed
        }
        Text(
            listOf(badge, connectionNote).filter { it.isNotEmpty() }.joinToString(" · "),
            style = NfType.Body.copy(fontSize = 13.sp),
            color = color,
            modifier = Modifier.width(SettingWidth).padding(bottom = 12.dp),
        )
        when {
            loadError != null -> {
                Text(loadError, style = NfType.Body, color = WarnOrange, modifier = Modifier.width(SettingWidth))
                NfButton(strings().retry, onClick = onRetry, modifier = Modifier.focusRequester(retry))
                LaunchedEffect(Unit) {
                    withFrameNanos { }
                    retry.tryFocus()
                }
            }
            loading -> Spinner(Modifier.padding(top = 16.dp))
            else -> {
                rows()
                Text(
                    notice?.second.orEmpty(),
                    style = NfType.Body.copy(fontSize = 13.sp),
                    color = if (notice?.first == true) OkGreen else FailRed,
                    modifier = Modifier.width(SettingWidth).heightIn(min = 20.dp).padding(top = 4.dp),
                )
                LaunchedEffect(Unit) {
                    withFrameNanos { }
                    initialFocus.tryFocus()
                }
            }
        }
    }
}

/** The write-only key: "Saved" / "Not set", or what Save will do with it. */
@Composable
private fun KeyRow(
    s: AccountStrings,
    hint: String,
    stored: Boolean,
    typed: String,
    cleared: Boolean,
    modifier: Modifier = Modifier,
    onClick: () -> Unit,
) {
    val (value, color) = when {
        typed.isNotBlank() -> s.keyNew to Color.White
        cleared -> s.keyRemoved to WarnOrange
        stored -> s.keySaved to OkGreen
        else -> s.keyNotSet to NfColors.Dim
    }
    SettingRow(s.apiKey, onClick = onClick, hint = hint, value = value, valueColor = color, modifier = modifier)
}

@Composable
private fun Buttons(
    saveLabel: String,
    canSave: Boolean,
    onSave: () -> Unit,
    testLabel: String,
    canTest: Boolean,
    onTest: () -> Unit,
) {
    Row(Modifier.padding(top = 10.dp), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
        NfButton(saveLabel, onClick = onSave, enabled = canSave)
        NfButton(testLabel, onClick = onTest, enabled = canTest, style = ButtonStyle.Secondary)
    }
}

/**
 * Text entry with the on-screen keyboard. A secret is masked until "Show"; a stored secret is never shown
 * (the server does not send it), only replaced or removed.
 */
@Composable
private fun TextEditor(
    label: String,
    initial: String,
    secret: Boolean,
    onDone: (String) -> Unit,
    onCancel: () -> Unit,
    hint: String? = null,
    stored: Boolean = false,
    onRemove: (() -> Unit)? = null,
) {
    val s = accountStrings.current()
    var text by rememberSaveable { mutableStateOf(initial) }
    var show by rememberSaveable { mutableStateOf(!secret) }
    val firstKey = remember { FocusRequester() }
    BackHandler(onBack = onCancel)

    Column(Modifier.padding(start = Dimens.ScreenPadH, top = 56.dp, end = 40.dp)) {
        Text(label, style = NfType.PageTitle.copy(fontSize = 29.sp))
        if (hint != null) Text(hint, style = NfType.Body.copy(fontSize = 13.sp), color = NfColors.Muted, modifier = Modifier.padding(top = 6.dp))
        Row(Modifier.padding(top = 24.dp)) {
            Column(Modifier.width(380.dp)) {
                KeyboardField(
                    if (show) text else "•".repeat(text.length),
                    Modifier.fillMaxWidth(),
                    placeholder = if (stored) s.storedPlaceholder else "",
                )
                Row(Modifier.padding(top = 20.dp), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                    NfButton(s.done, onClick = { onDone(text.trim()) })
                    NfButton(strings().cancel, onClick = onCancel, style = ButtonStyle.Secondary)
                }
                if (secret || (stored && onRemove != null)) {
                    Row(Modifier.padding(top = 12.dp), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                        if (secret) NfButton(if (show) s.hide else s.show, onClick = { show = !show }, style = ButtonStyle.Secondary)
                        if (stored && onRemove != null) NfButton(s.remove, onClick = onRemove, style = ButtonStyle.Secondary)
                    }
                }
            }
            Column(Modifier.padding(start = 32.dp)) {
                OnScreenKeyboard(KeyboardLayout.Url, text, { text = it }, width = 380.dp, max = 200, firstKey = firstKey)
            }
        }
    }
    LaunchedEffect(Unit) {
        withFrameNanos { }
        firstKey.tryFocus()
    }
}
