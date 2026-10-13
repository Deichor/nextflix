package com.nextflix.tv.core.ui.components

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.gestures.BringIntoViewSpec
import androidx.compose.foundation.gestures.LocalBringIntoViewSpec
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.remember
import androidx.compose.ui.ExperimentalComposeUiApi
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.focusRestorer
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.tv.material3.Text
import com.nextflix.tv.core.ui.focus.restorableFocus
import com.nextflix.tv.core.ui.theme.Dimens
import com.nextflix.tv.core.ui.theme.NfType

/**
 * Scrolls a focused child so its leading edge rests [padding] after the container's start (rows: the focused
 * card stays at the left edge like Netflix; columns of rows: the focused row stays at the top).
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun rememberStartAlignedBringIntoView(padding: Dp): BringIntoViewSpec {
    val px = with(LocalDensity.current) { padding.toPx() }
    return remember(px) {
        object : BringIntoViewSpec {
            override fun calculateScrollDistance(offset: Float, size: Float, containerSize: Float): Float = offset - px
        }
    }
}

/**
 * A titled horizontal row of focusable items (webOS components/Row.tsx).
 *
 * - [items] null: loading (skeleton cards); empty: the row is not shown.
 * - Every item gets an [itemModifier][itemContent] with restorableFocus("$rowKey:${key(item)}") applied, so Back
 *   from Detail / Player focuses the same card again (use FocusRestore on the screen).
 * - Focus coming back into the row (Up / Down) returns to the card last focused in it; the focused card is kept
 *   at the left edge.
 */
@OptIn(ExperimentalFoundationApi::class, ExperimentalComposeUiApi::class)
@Composable
fun <T> ContentRow(
    title: String,
    items: List<T>?,
    rowKey: String,
    key: (T) -> Any,
    modifier: Modifier = Modifier,
    startPadding: Dp = Dimens.ScreenPadH,
    skeleton: @Composable () -> Unit = { CardSkeleton() },
    itemContent: @Composable (index: Int, item: T, itemModifier: Modifier) -> Unit,
) {
    if (items != null && items.isEmpty()) return
    val state = rememberLazyListState()
    Column(modifier) {
        if (title.isNotEmpty()) {
            Text(
                title,
                style = NfType.RowTitle,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.padding(start = startPadding, bottom = Dimens.RowTitleGap),
            )
        }
        CompositionLocalProvider(LocalBringIntoViewSpec provides rememberStartAlignedBringIntoView(startPadding)) {
            LazyRow(
                state = state,
                modifier = Modifier.focusRestorer(),
                contentPadding = PaddingValues(start = startPadding, end = 480.dp, top = 10.dp, bottom = 12.dp),
                horizontalArrangement = Arrangement.spacedBy(Dimens.CardGap),
            ) {
                if (items == null) {
                    this.items(6, contentType = { "skeleton" }) { skeleton() }
                } else {
                    itemsIndexed(items, key = { _, item -> key(item) }, contentType = { _, _ -> "card" }) { index, item ->
                        itemContent(index, item, Modifier.restorableFocus("$rowKey:${key(item)}"))
                    }
                }
            }
        }
    }
}
