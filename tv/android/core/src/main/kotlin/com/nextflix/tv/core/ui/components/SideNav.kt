package com.nextflix.tv.core.ui.components

import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.ExperimentalComposeUiApi
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusDirection
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.foundation.focusGroup
import androidx.compose.ui.focus.focusProperties
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.tv.material3.ClickableSurfaceDefaults
import androidx.tv.material3.Icon
import androidx.tv.material3.Surface
import androidx.tv.material3.Text
import com.nextflix.tv.core.i18n.CoreStrings
import com.nextflix.tv.core.i18n.strings
import com.nextflix.tv.core.model.Profile
import com.nextflix.tv.core.nav.Tab
import com.nextflix.tv.core.ui.focus.tryFocus
import com.nextflix.tv.core.ui.icons.Lucide
import com.nextflix.tv.core.ui.theme.Dimens
import com.nextflix.tv.core.ui.theme.NfColors
import com.nextflix.tv.core.ui.theme.NfType

private fun Tab.icon(): ImageVector = when (this) {
    Tab.Search -> Lucide.Search
    Tab.Home -> Lucide.House
    Tab.TvShows -> Lucide.Tv
    Tab.Movies -> Lucide.Film
    Tab.Settings -> Lucide.Settings
}

private fun Tab.label(s: CoreStrings): String = when (this) {
    Tab.Search -> s.navSearch
    Tab.Home -> s.navHome
    Tab.TvShows -> s.navTvShows
    Tab.Movies -> s.navMovies
    Tab.Settings -> s.navSettings
}

/**
 * Netflix-style rail on the left (webOS components/SideNav.tsx), drawn over the screen. It widens while it has
 * focus; entering it always lands on the [active] tab ([activeRequester] focuses it directly, e.g. Back on Home).
 */
@OptIn(ExperimentalComposeUiApi::class)
@Composable
fun SideNav(
    active: Tab,
    profile: Profile?,
    onSelect: (Tab) -> Unit,
    onProfile: () -> Unit,
    activeRequester: FocusRequester,
    modifier: Modifier = Modifier,
    onFocusChange: (Boolean) -> Unit = {},
) {
    var open by remember { mutableStateOf(false) }
    val width by animateDpAsState(if (open) Dimens.RailOpen else Dimens.RailClosed, tween(200), label = "rail")
    val s = strings()
    Column(
        modifier
            .fillMaxHeight()
            .width(width)
            .background(
                if (open) {
                    Brush.horizontalGradient(listOf(Color(0xF2000000), Color(0xD9000000)))
                } else {
                    Brush.horizontalGradient(0f to NfColors.Background, 0.65f to NfColors.Background.copy(alpha = 0.92f), 1f to Color.Transparent)
                },
            )
            .onFocusChanged {
                open = it.hasFocus
                onFocusChange(it.hasFocus)
            }
            .focusProperties { onEnter = { if (requestedFocusDirection != FocusDirection.Enter) activeRequester.tryFocus() } }
            .focusGroup()
            .padding(start = 10.dp, end = 10.dp, top = 24.dp),
    ) {
        if (profile != null) {
            RailItem(
                label = profile.name,
                open = open,
                selected = false,
                onClick = onProfile,
                leading = { Avatar(profile.avatar, profile.color, profile.name, size = 22.dp) },
            )
        }
        Spacer(Modifier.height(40.dp))
        Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Tab.entries.forEach { tab ->
                RailItem(
                    label = tab.label(s),
                    open = open,
                    selected = tab == active,
                    onClick = { if (tab != active) onSelect(tab) },
                    modifier = if (tab == active) Modifier.focusRequester(activeRequester) else Modifier,
                    leading = { Icon(tab.icon(), contentDescription = null, modifier = Modifier.size(18.dp)) },
                )
            }
        }
    }
}

@Composable
private fun RailItem(
    label: String,
    open: Boolean,
    selected: Boolean,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    leading: @Composable () -> Unit,
) {
    Surface(
        onClick = onClick,
        modifier = modifier.fillMaxWidth().height(Dimens.ButtonHeight),
        shape = ClickableSurfaceDefaults.shape(RoundedCornerShape(4.dp)),
        colors = ClickableSurfaceDefaults.colors(
            containerColor = Color.Transparent,
            contentColor = if (selected) Color.White else NfColors.Dim,
            focusedContainerColor = Color.White,
            focusedContentColor = Color.Black,
        ),
        scale = ClickableSurfaceDefaults.scale(focusedScale = 1f),
    ) {
        Row(Modifier.padding(horizontal = 6.dp).fillMaxHeight(), verticalAlignment = Alignment.CenterVertically) {
            leading()
            if (open) {
                Spacer(Modifier.width(13.dp))
                Text(label, style = NfType.Button, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        }
    }
}
