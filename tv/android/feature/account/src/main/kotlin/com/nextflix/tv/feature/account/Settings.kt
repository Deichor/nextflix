package com.nextflix.tv.feature.account

import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.tv.material3.Text
import com.nextflix.tv.core.Core
import com.nextflix.tv.core.core
import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.i18n.LocalAppLanguage
import com.nextflix.tv.core.i18n.strings
import com.nextflix.tv.core.model.ProfileInput
import com.nextflix.tv.core.nav.Navigator
import com.nextflix.tv.core.nav.Route
import com.nextflix.tv.core.prefs.DeviceSettings
import com.nextflix.tv.core.prefs.HdrMode
import com.nextflix.tv.core.prefs.PlayerPrefs
import com.nextflix.tv.core.prefs.SubtitleStyle
import com.nextflix.tv.core.prefs.subtitleStyleStrings
import com.nextflix.tv.core.ui.components.LocalSubtitleSample
import com.nextflix.tv.core.ui.focus.FocusRestore
import com.nextflix.tv.core.ui.focus.restorableFocus
import com.nextflix.tv.core.ui.focus.tryFocus
import com.nextflix.tv.core.ui.theme.Dimens
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

internal data class InfoLines(val device: String, val caps: String, val version: String)

internal data class LanguageUi(val saving: Boolean = false, val error: String? = null)

internal class SettingsViewModel(private val core: Core) : ViewModel() {
    private val pid = core.session.profileId
    val device: StateFlow<DeviceSettings> = core.devicePrefs.settings
    val prefs: StateFlow<PlayerPrefs> = core.playerPrefs.all
        .map { core.playerPrefs.get(pid) }
        .stateIn(viewModelScope, SharingStarted.Eagerly, core.playerPrefs.get(pid))

    private val _info = MutableStateFlow<InfoLines?>(null)
    val info: StateFlow<InfoLines?> = _info.asStateFlow()
    private val _language = MutableStateFlow(LanguageUi())
    val language: StateFlow<LanguageUi> = _language.asStateFlow()

    init {
        refreshInfo()
    }

    private fun refreshInfo() {
        viewModelScope.launch(Dispatchers.Default) {
            val ctx = core.appContext
            _info.value = InfoLines(deviceLine(ctx), capsLine(core.playbackCaps.current()), appVersion(ctx))
        }
    }

    // Writes run on the app scope so leaving the screen right away does not cut them off

    fun cycleHdr() {
        val next = HdrMode.entries[(device.value.hdrMode.ordinal + 1) % HdrMode.entries.size]
        core.scope.launch {
            core.devicePrefs.setHdrMode(next)
            core.devicePrefs.settings.first { it.hdrMode == next }
            core.playbackCaps.invalidate()
            refreshInfo()
        }
    }

    fun toggleIntroAnimation() {
        val on = !device.value.introAnimation
        core.scope.launch { core.devicePrefs.setIntroAnimation(on) }
    }

    fun toggleIntroSound() {
        val on = !device.value.introSound
        core.scope.launch { core.devicePrefs.setIntroSound(on) }
    }

    fun toggleAutoplay() {
        core.scope.launch { core.playerPrefs.update(pid) { it.copy(autoplayNext = !it.autoplayNext) } }
    }

    fun cycleSubtitle(option: SubtitleStyle.Option) {
        core.scope.launch { core.playerPrefs.update(pid) { it.copy(subtitleStyle = it.subtitleStyle.cycle(option, 1)) } }
    }

    fun resetSubtitle() {
        core.scope.launch { core.playerPrefs.update(pid) { it.copy(subtitleStyle = SubtitleStyle()) } }
    }

    /** Applied at once (the UI follows the active profile), then saved; reverted if saving fails. */
    fun toggleLanguage() {
        val previous = core.session.profile ?: return
        if (_language.value.saving) return
        val next = if (previous.appLanguage == AppLanguage.Tr) AppLanguage.En else AppLanguage.Tr
        _language.value = LanguageUi(saving = true)
        core.scope.launch {
            core.session.setProfile(previous.copy(language = next.code))
            try {
                val updated = core.profiles.update(previous.id, ProfileInput(previous.name, previous.avatar, previous.color, next.code))
                if (core.session.profileId == updated.id) {
                    core.session.setProfile(updated.copy(language = updated.language ?: next.code))
                }
                _language.value = LanguageUi()
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                if (core.session.profileId == previous.id) core.session.setProfile(previous)
                _language.value = LanguageUi(error = e.describe(accountStrings[core.session.language]))
            }
        }
    }
}

internal enum class ServerPanel { Indexer, Debrid }

/** Settings tab (webOS screens/SettingsScreen.tsx) plus the server's indexer and TorBox settings. */
@Composable
internal fun SettingsContent(nav: Navigator) {
    val core = core()
    val vm = viewModel { SettingsViewModel(core) }
    var panel by rememberSaveable { mutableStateOf<ServerPanel?>(null) }
    var closedPanel by remember { mutableStateOf<ServerPanel?>(null) }
    val scroll = rememberScrollState()
    val panelRows = remember { ServerPanel.entries.associateWith { FocusRequester() } }

    val open = panel
    if (open != null) {
        val close = {
            closedPanel = open
            panel = null
        }
        when (open) {
            ServerPanel.Indexer -> IndexerPanel(onClose = close)
            ServerPanel.Debrid -> DebridPanel(onClose = close)
        }
    } else {
        SettingsList(vm, nav, scroll, panelRows, onOpen = { panel = it })
        LaunchedEffect(closedPanel) {
            val row = closedPanel?.let { panelRows[it] } ?: return@LaunchedEffect
            withFrameNanos { }
            row.tryFocus()
            closedPanel = null
        }
    }
    FocusRestore(ready = true, fallbackKey = "settings-server")
}

@Composable
private fun SettingsList(
    vm: SettingsViewModel,
    nav: Navigator,
    scroll: ScrollState,
    panelRows: Map<ServerPanel, FocusRequester>,
    onOpen: (ServerPanel) -> Unit,
) {
    val core = core()
    val s = accountStrings.current()
    val common = strings()
    val language = LocalAppLanguage.current
    val device by vm.device.collectAsStateWithLifecycle()
    val prefs by vm.prefs.collectAsStateWithLifecycle()
    val info by vm.info.collectAsStateWithLifecycle()
    val languageUi by vm.language.collectAsStateWithLifecycle()
    val subStrings = subtitleStyleStrings[language]
    var subFocus by remember { mutableStateOf(false) }
    val onOff = { on: Boolean -> if (on) common.on else common.off }

    Box(Modifier.fillMaxSize()) {
        Column(
            Modifier
                .fillMaxHeight()
                .padding(start = Dimens.ScreenPadH)
                .verticalScroll(scroll)
                .padding(top = 56.dp, bottom = 40.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Text(s.settingsTitle, style = NfType.PageTitle.copy(fontSize = 29.sp), modifier = Modifier.padding(bottom = 16.dp))
            SettingRow(
                s.server,
                onClick = { nav.push(Route.Setup) },
                value = core.session.server.orEmpty(),
                modifier = Modifier.restorableFocus("settings-server"),
            )
            if (core.session.profile != null) {
                SettingRow(
                    s.displayLanguage,
                    onClick = vm::toggleLanguage,
                    value = if (language == AppLanguage.Tr) s.languageTr else s.languageEn,
                    modifier = Modifier.restorableFocus("settings-language"),
                )
                languageUi.error?.let {
                    Text(s.languageSaveFailed(it), style = NfType.BodySmall, color = WarnOrange, modifier = Modifier.width(SettingWidth))
                }
            }
            SettingRow(
                s.hdr,
                onClick = vm::cycleHdr,
                value = when (device.hdrMode) {
                    HdrMode.Auto -> s.hdrAuto
                    HdrMode.On -> s.hdrOn
                    HdrMode.Off -> s.hdrOff
                },
                modifier = Modifier.restorableFocus("settings-hdr"),
            )
            SettingRow(
                s.autoplayNext,
                onClick = vm::toggleAutoplay,
                value = onOff(prefs.autoplayNext),
                modifier = Modifier.restorableFocus("settings-autoplay"),
            )
            SettingRow(
                s.introAnimation,
                onClick = vm::toggleIntroAnimation,
                value = onOff(device.introAnimation),
                modifier = Modifier.restorableFocus("settings-intro"),
            )
            SettingRow(
                s.introSound,
                onClick = vm::toggleIntroSound,
                value = onOff(device.introSound),
                modifier = Modifier.restorableFocus("settings-intro-sound"),
            )

            SectionTitle(subStrings.title)
            Column(
                Modifier.onFocusChanged { subFocus = it.hasFocus },
                verticalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                SubtitleStyle.Option.entries.forEach { option ->
                    SettingRow(
                        SubtitleStyle.optionLabel(option, language),
                        onClick = { vm.cycleSubtitle(option) },
                        value = prefs.subtitleStyle.valueLabel(option, language),
                        modifier = Modifier.restorableFocus("settings-sub-${option.name}"),
                    )
                }
                SettingRow(subStrings.reset, onClick = vm::resetSubtitle, modifier = Modifier.restorableFocus("settings-sub-reset"))
            }

            SectionTitle(s.advanced)
            Text(
                s.advancedIntro,
                style = NfType.BodySmall,
                color = NfColors.Dim,
                modifier = Modifier.width(SettingWidth).padding(bottom = 4.dp),
            )
            SettingRow(
                s.indexerTitle,
                onClick = { onOpen(ServerPanel.Indexer) },
                hint = s.indexerSummary,
                value = "›",
                modifier = Modifier.restorableFocus("settings-indexer").focusRequester(panelRows.getValue(ServerPanel.Indexer)),
            )
            SettingRow(
                s.debridTitle,
                onClick = { onOpen(ServerPanel.Debrid) },
                hint = s.debridSummary,
                value = "›",
                modifier = Modifier.restorableFocus("settings-debrid").focusRequester(panelRows.getValue(ServerPanel.Debrid)),
            )

            info?.let { InfoBlock(it, s) }
        }

        if (subFocus) {
            SubtitlePreview(
                text = subStrings.sample,
                style = prefs.subtitleStyle,
                modifier = Modifier.align(Alignment.TopEnd).padding(top = 132.dp, end = 20.dp),
            )
        }
    }
}

@Composable
private fun InfoBlock(info: InfoLines, s: AccountStrings) {
    Column(Modifier.width(SettingWidth).padding(top = 16.dp)) {
        listOf(s.device to info.device, s.caps to info.caps, s.version to info.version).forEach { (label, value) ->
            Text(
                label,
                style = NfType.Body.copy(fontSize = 12.sp, fontWeight = FontWeight.SemiBold),
                color = NfColors.Muted,
                modifier = Modifier.padding(top = 11.dp),
            )
            Text(value, style = NfType.Body.copy(fontSize = 12.sp), color = NfColors.Dim, modifier = Modifier.padding(top = 2.dp))
        }
    }
}

/**
 * Live preview of the subtitle appearance on a small "video" (webOS .sub-preview). LocalSubtitleSample (the
 * subtitles module) draws the line, the dimming band and the position, scaled to this box.
 */
@Composable
private fun SubtitlePreview(text: String, style: SubtitleStyle, modifier: Modifier = Modifier) {
    val sample = LocalSubtitleSample.current
    Box(
        modifier
            .size(216.dp, 122.dp)
            .clip(RoundedCornerShape(5.dp))
            .background(
                Brush.linearGradient(
                    0f to Color(0xFF4A5D70),
                    0.55f to Color(0xFF26313D),
                    1f to Color(0xFF0D1115),
                ),
            ),
    ) {
        sample.Render(text, style, Modifier.matchParentSize())
    }
}
