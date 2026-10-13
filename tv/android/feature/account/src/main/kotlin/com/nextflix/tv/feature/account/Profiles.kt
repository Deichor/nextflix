package com.nextflix.tv.feature.account

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
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
import com.nextflix.tv.core.i18n.strings
import com.nextflix.tv.core.model.Profile
import com.nextflix.tv.core.nav.Navigator
import com.nextflix.tv.core.nav.Route
import com.nextflix.tv.core.ui.components.Avatar
import com.nextflix.tv.core.ui.components.ButtonStyle
import com.nextflix.tv.core.ui.components.NfButton
import com.nextflix.tv.core.ui.components.Spinner
import com.nextflix.tv.core.ui.components.Wordmark
import com.nextflix.tv.core.ui.focus.FocusRestore
import com.nextflix.tv.core.ui.focus.restorableFocus
import com.nextflix.tv.core.ui.focus.tryFocus
import com.nextflix.tv.core.ui.icons.Lucide
import com.nextflix.tv.core.ui.theme.Inter
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

internal data class ProfilesState(
    /** null while loading */
    val list: List<Profile>? = null,
    val error: Throwable? = null,
    /** A profile that appeared since the last load (just added): it gets the focus. */
    val created: Int? = null,
    /** Retry removed the focused button: focus the first tile once the answer is in. */
    val refocus: Boolean = false,
)

internal class ProfilesViewModel(private val core: Core) : ViewModel() {
    private val _state = MutableStateFlow(ProfilesState())
    val state: StateFlow<ProfilesState> = _state.asStateFlow()
    private var seen: Set<Int>? = null
    private var shown = false
    private var job: Job? = null

    init {
        load()
    }

    /** The screen is on top again (Back from Add Profile or Setup): reload without a spinner. */
    fun onShown() {
        if (shown) load() else shown = true
    }

    fun retry() {
        _state.value = ProfilesState(refocus = true)
        load()
    }

    fun consumeFocusHints() = _state.update { it.copy(created = null, refocus = false) }

    private fun load() {
        job?.cancel()
        job = viewModelScope.launch {
            try {
                val list = core.profiles.list()
                val before = seen
                seen = list.map { it.id }.toSet()
                val created = before?.let { ids -> list.firstOrNull { it.id !in ids }?.id }
                _state.update { ProfilesState(list, null, created, it.refocus) }
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                _state.update { ProfilesState(emptyList(), e, refocus = it.refocus) }
            }
        }
    }

    fun pick(profile: Profile, then: () -> Unit) {
        viewModelScope.launch {
            core.session.setProfile(profile)
            then()
        }
    }
}

/** "Who's watching?" (webOS screens/ProfilesScreen.tsx). */
@Composable
internal fun ProfilesContent(nav: Navigator) {
    val core = core()
    val vm = viewModel { ProfilesViewModel(core) }
    val state by vm.state.collectAsStateWithLifecycle()
    val s = accountStrings.current()
    LaunchedEffect(Unit) { vm.onShown() }

    val list = state.list
    val canAdd = list != null && state.error == null && list.size < Profile.MAX_PROFILES
    val last = core.session.profileId
    val fallbackKey = when {
        list == null -> null
        list.any { it.id == last } -> "profile-$last"
        list.isNotEmpty() -> "profile-${list[0].id}"
        canAdd -> "profile-add"
        else -> "profiles-retry"
    }
    val requesters = remember { HashMap<String, FocusRequester>() }
    fun requester(key: String) = requesters.getOrPut(key) { FocusRequester() }
    var serverFocused by remember { mutableStateOf(false) }

    Box(Modifier.fillMaxSize()) {
        Wordmark(Modifier.padding(start = 32.dp, top = 24.dp))
        Column(
            Modifier.fillMaxSize(),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.Center,
        ) {
            Text(s.whoIsWatching, style = NfType.PageTitle.copy(fontSize = 32.sp, lineHeight = 38.sp, fontWeight = FontWeight.SemiBold))
            if (list == null) {
                Spinner(Modifier.padding(top = 40.dp))
            } else if (list.isNotEmpty() || canAdd) {
                Row(Modifier.padding(top = 40.dp)) {
                    list.forEach { p ->
                        ProfileTile(
                            name = p.name,
                            onClick = { vm.pick(p) { nav.reset(Route.Home) } },
                            modifier = Modifier.restorableFocus("profile-${p.id}").focusRequester(requester("profile-${p.id}")),
                        ) { Avatar(p.avatar, p.color, p.name, size = 104.dp) }
                    }
                    if (canAdd) {
                        ProfileTile(
                            name = s.addProfile,
                            onClick = { nav.push(Route.AddProfile(list.size)) },
                            modifier = Modifier.restorableFocus("profile-add").focusRequester(requester("profile-add")),
                        ) { focused ->
                            Box(
                                Modifier
                                    .size(104.dp)
                                    .background(if (focused) Color(0xFF4A4A4A) else Color(0xFF2A2A2A), RoundedCornerShape(8.dp)),
                                contentAlignment = Alignment.Center,
                            ) {
                                Icon(
                                    Lucide.Plus,
                                    contentDescription = null,
                                    tint = if (focused) Color.White else NfColors.Dim,
                                    modifier = Modifier.size(57.dp),
                                )
                            }
                        }
                    }
                }
            }
            val error = state.error
            if (error != null) {
                Text(
                    s.couldNotReach(error.describe(s)),
                    style = NfType.Body.copy(fontSize = 13.5.sp),
                    color = NfColors.Muted,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.padding(top = 32.dp).width(560.dp),
                )
                Spacer(Modifier.height(16.dp))
                NfButton(
                    strings().retry,
                    onClick = vm::retry,
                    modifier = Modifier.restorableFocus("profiles-retry").focusRequester(requester("profiles-retry")),
                )
            }
            NfButton(
                s.changeServer,
                onClick = { nav.push(Route.Setup) },
                icon = Lucide.Settings,
                style = ButtonStyle.Ghost,
                modifier = Modifier
                    .padding(top = 48.dp)
                    .restorableFocus("profiles-server")
                    .onFocusChanged { serverFocused = it.hasFocus },
            )
        }
    }

    FocusRestore(ready = list != null, fallbackKey = fallbackKey)

    // A new profile, or the first tile after Retry (unless the viewer already moved on)
    LaunchedEffect(state.created, state.refocus, list) {
        if (list == null || (state.created == null && !state.refocus)) return@LaunchedEffect
        withFrameNanos { }
        val key = state.created?.let { "profile-$it" } ?: fallbackKey.takeUnless { serverFocused }
        key?.let { requesters[it]?.tryFocus() }
        vm.consumeFocusHints()
    }
}

// Outer corner of the focus frame: the avatar's corner (8.3 dp) plus the frame width, so the frame hugs it
private val FrameShape = RoundedCornerShape(11.dp)

/** Avatar tile: white frame and a slight zoom when focused, the name brightens. */
@Composable
private fun ProfileTile(
    name: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    picture: @Composable (focused: Boolean) -> Unit,
) {
    var focused by remember { mutableStateOf(false) }
    Column(
        Modifier.padding(horizontal = 13.dp).width(112.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        // Surface clips its content, so the zoom is its own scale, not a scale inside it
        Surface(
            onClick = onClick,
            modifier = modifier.onFocusChanged { focused = it.isFocused },
            shape = ClickableSurfaceDefaults.shape(FrameShape),
            colors = ClickableSurfaceDefaults.colors(
                containerColor = Color.Transparent,
                focusedContainerColor = Color.Transparent,
                pressedContainerColor = Color.Transparent,
            ),
            scale = ClickableSurfaceDefaults.scale(focusedScale = 1.08f),
        ) {
            Box(
                Modifier
                    .border(BorderStroke(2.8.dp, if (focused) Color.White else Color.Transparent), FrameShape)
                    .padding(2.8.dp),
            ) { picture(focused) }
        }
        Text(
            name,
            fontFamily = Inter,
            fontSize = 14.5.sp,
            color = if (focused) Color.White else NfColors.Dim,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(top = 11.dp),
        )
    }
}
