package com.nextflix.tv.feature.account

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.focusGroup
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.ExperimentalComposeUiApi
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.focusRestorer
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Shape
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.tv.material3.ClickableSurfaceDefaults
import androidx.tv.material3.Icon
import androidx.tv.material3.Surface
import androidx.tv.material3.Text
import com.nextflix.tv.core.Core
import com.nextflix.tv.core.core
import com.nextflix.tv.core.i18n.AppLanguage
import com.nextflix.tv.core.i18n.LocalAppLanguage
import com.nextflix.tv.core.i18n.strings
import com.nextflix.tv.core.model.Profile
import com.nextflix.tv.core.model.ProfileInput
import com.nextflix.tv.core.nav.Navigator
import com.nextflix.tv.core.nav.Route
import com.nextflix.tv.core.net.ApiException
import com.nextflix.tv.core.ui.components.Avatar
import com.nextflix.tv.core.ui.components.AvatarKeys
import com.nextflix.tv.core.ui.components.ButtonStyle
import com.nextflix.tv.core.ui.components.KeyboardField
import com.nextflix.tv.core.ui.components.KeyboardLayout
import com.nextflix.tv.core.ui.components.NfButton
import com.nextflix.tv.core.ui.components.NfChip
import com.nextflix.tv.core.ui.components.OnScreenKeyboard
import com.nextflix.tv.core.ui.components.ProfileColors
import com.nextflix.tv.core.ui.components.Wordmark
import com.nextflix.tv.core.ui.components.profileColor
import com.nextflix.tv.core.ui.focus.FocusRestore
import com.nextflix.tv.core.ui.icons.Lucide
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

internal data class AddProfileState(
    val name: String,
    val avatar: String,
    val color: String,
    val language: AppLanguage,
    val saving: Boolean = false,
    /** The server's message, or "" for the generic text */
    val error: String? = null,
)

internal class AddProfileViewModel(private val core: Core, count: Int, language: AppLanguage) : ViewModel() {
    private val _state = MutableStateFlow(
        AddProfileState(
            name = "",
            avatar = AvatarKeys[count % AvatarKeys.size],
            color = ProfileColors[count % ProfileColors.size],
            language = language,
        ),
    )
    val state: StateFlow<AddProfileState> = _state.asStateFlow()

    fun setName(v: String) = _state.update { it.copy(name = v, error = null) }
    fun setAvatar(v: String) = _state.update { it.copy(avatar = v) }
    fun setColor(v: String) = _state.update { it.copy(color = v) }
    fun setLanguage(v: AppLanguage) = _state.update { it.copy(language = v) }

    fun save(onSaved: () -> Unit) {
        val st = _state.value
        val name = st.name.trim()
        if (name.isEmpty() || st.saving) return
        _state.update { it.copy(saving = true, error = null) }
        viewModelScope.launch {
            try {
                core.profiles.create(ProfileInput(name.take(Profile.MAX_NAME), st.avatar, st.color, st.language.code))
                onSaved()
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                _state.update { it.copy(saving = false, error = (e as? ApiException.Http)?.message.orEmpty()) }
            }
        }
    }
}

/** Netflix "Add Profile" (webOS screens/AddProfileScreen.tsx): name, avatar, color and language. */
@OptIn(ExperimentalLayoutApi::class, ExperimentalComposeUiApi::class)
@Composable
internal fun AddProfileContent(route: Route.AddProfile, nav: Navigator) {
    val core = core()
    val language = LocalAppLanguage.current
    val vm = viewModel { AddProfileViewModel(core, route.existing, language) }
    val state by vm.state.collectAsStateWithLifecycle()
    val s = accountStrings.current()
    val common = strings()
    val firstKey = remember { FocusRequester() }
    val activeAvatar = remember(state.avatar) { FocusRequester() }
    val activeColor = remember(state.color) { FocusRequester() }
    val activeLang = remember(state.language) { FocusRequester() }
    val trimmed = state.name.trim()

    Box(Modifier.fillMaxSize()) {
        Wordmark(Modifier.padding(start = 32.dp, top = 24.dp))
        Column(Modifier.fillMaxSize().padding(top = 44.dp), horizontalAlignment = Alignment.CenterHorizontally) {
            Text(s.addProfile, style = NfType.PageTitle.copy(fontSize = 29.sp, fontWeight = FontWeight.SemiBold))
            Text(s.addProfileHint, style = NfType.Body.copy(fontSize = 13.sp), color = NfColors.Dim, modifier = Modifier.padding(top = 6.dp))
            Row(Modifier.padding(top = 24.dp)) {
                Column(Modifier.width(400.dp)) {
                    Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(bottom = 16.dp)) {
                        Avatar(state.avatar, state.color, trimmed, size = 64.dp)
                        Spacer(Modifier.width(16.dp))
                        Column(Modifier.weight(1f)) {
                            KeyboardField(state.name, Modifier.fillMaxWidth(), placeholder = s.name)
                            Text(
                                "${state.name.length}/${Profile.MAX_NAME}",
                                style = NfType.BodySmall,
                                color = NfColors.Dim,
                                textAlign = TextAlign.End,
                                modifier = Modifier.fillMaxWidth().padding(top = 4.dp),
                            )
                        }
                    }
                    OnScreenKeyboard(
                        KeyboardLayout.Text,
                        state.name,
                        vm::setName,
                        width = 400.dp,
                        max = Profile.MAX_NAME,
                        firstKey = firstKey,
                    )
                }
                Spacer(Modifier.width(40.dp))
                Column(Modifier.width(400.dp)) {
                    FieldLabel(s.avatar, top = 0)
                    FlowRow(
                        Modifier.focusRestorer(activeAvatar).focusGroup(),
                        horizontalArrangement = Arrangement.spacedBy(7.dp),
                        verticalArrangement = Arrangement.spacedBy(7.dp),
                    ) {
                        AvatarKeys.forEach { key ->
                            PickerOption(
                                active = key == state.avatar,
                                onClick = { vm.setAvatar(key) },
                                size = 35.dp,
                                shape = RoundedCornerShape(3.dp),
                                modifier = if (key == state.avatar) Modifier.focusRequester(activeAvatar) else Modifier,
                            ) { Avatar(key, state.color, "", size = 35.dp) }
                        }
                    }
                    FieldLabel(s.color)
                    Row(Modifier.focusRestorer(activeColor).focusGroup(), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                        ProfileColors.forEach { c ->
                            PickerOption(
                                active = c == state.color,
                                onClick = { vm.setColor(c) },
                                size = 30.dp,
                                shape = CircleShape,
                                dimInactive = false,
                                modifier = if (c == state.color) Modifier.focusRequester(activeColor) else Modifier,
                            ) {
                                Box(Modifier.size(30.dp), contentAlignment = Alignment.Center) {
                                    Box(Modifier.size(30.dp).background(profileColor(c), CircleShape))
                                    if (c == state.color) Icon(Lucide.Check, null, tint = Color.White, modifier = Modifier.size(16.dp))
                                }
                            }
                        }
                    }
                    FieldLabel(s.language)
                    Row(Modifier.focusRestorer(activeLang).focusGroup(), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                        AppLanguage.entries.forEach { l ->
                            NfChip(
                                text = if (l == AppLanguage.Tr) s.languageTr else s.languageEn,
                                selected = l == state.language,
                                onClick = { vm.setLanguage(l) },
                                modifier = if (l == state.language) Modifier.focusRequester(activeLang) else Modifier,
                            )
                        }
                    }
                    Row(Modifier.padding(top = 27.dp), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                        NfButton(
                            if (state.saving) common.saving else common.save,
                            onClick = { vm.save { if (!nav.back()) nav.reset(Route.Profiles) } },
                            enabled = trimmed.isNotEmpty(),
                        )
                        NfButton(common.cancel, onClick = { nav.back() }, style = ButtonStyle.Secondary)
                    }
                    Text(
                        state.error?.ifEmpty { s.couldNotSaveProfile } ?: "",
                        style = NfType.Body.copy(fontSize = 13.sp),
                        color = WarnOrange,
                        modifier = Modifier.padding(top = 13.dp).heightIn(min = 18.dp),
                    )
                }
            }
        }
    }
    FocusRestore(ready = true, fallback = firstKey)
}

@Composable
private fun FieldLabel(text: String, top: Int = 18) {
    Text(
        text,
        style = NfType.Body.copy(fontSize = 12.sp),
        color = NfColors.Muted,
        modifier = Modifier.padding(top = top.dp, bottom = 6.dp),
    )
}

/** Avatar or color choice: the active one is ringed, the focused one zooms with a white ring. */
@Composable
private fun PickerOption(
    active: Boolean,
    onClick: () -> Unit,
    size: Dp,
    shape: Shape,
    modifier: Modifier = Modifier,
    dimInactive: Boolean = true,
    content: @Composable () -> Unit,
) {
    var focused by remember { mutableStateOf(false) }
    val ring = when {
        focused -> Color.White
        active -> Color.White.copy(alpha = 0.7f)
        else -> Color.Transparent
    }
    Surface(
        onClick = onClick,
        modifier = modifier.onFocusChanged { focused = it.isFocused }.size(size),
        shape = ClickableSurfaceDefaults.shape(shape),
        colors = ClickableSurfaceDefaults.colors(
            containerColor = Color.Transparent,
            focusedContainerColor = Color.Transparent,
            pressedContainerColor = Color.Transparent,
        ),
        scale = ClickableSurfaceDefaults.scale(focusedScale = 1.18f),
    ) {
        Box(
            Modifier
                .alpha(if (dimInactive && !active && !focused) 0.55f else 1f)
                .border(BorderStroke(if (focused) 2.dp else 1.6.dp, ring), shape),
        ) { content() }
    }
}
