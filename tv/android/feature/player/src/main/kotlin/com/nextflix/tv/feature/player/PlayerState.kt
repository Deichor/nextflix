package com.nextflix.tv.feature.player

import androidx.compose.runtime.Immutable
import com.nextflix.tv.core.model.Availability
import com.nextflix.tv.core.nav.PlayRequest

internal enum class Phase { Resolving, Ready, Error, Unreleased }

/** An error to show: one of the player's messages or the server's text. */
@Immutable
internal sealed interface PlayerError {
    data class Key(val msg: Msg) : PlayerError
    data class Text(val text: String) : PlayerError
}

/** The torrent file the stream plays. */
@Immutable
internal data class PlayingFile(val hash: String, val index: Int)

/** Everything the player UI shows (webOS engine.ts PlayerState); subtitles come from the SubtitleController. */
@Immutable
internal data class PlayerState(
    val meta: PlayRequest,
    val phase: Phase = Phase.Resolving,
    val resolveElapsedMs: Long = 0,
    val stage: Msg = Msg.FindingSource,
    val error: PlayerError? = null,
    val unreleased: Availability? = null,
    val duration: Double = 0.0,
    /** file time in whole-second steps (exact: PlayerEngine.positionNow) */
    val position: Double = 0.0,
    /** the viewer wants playback (false while paused, also during a start hold) */
    val playing: Boolean = false,
    val buffering: Boolean = true,
    /** waiting paused for a larger buffer */
    val holding: Boolean = false,
    val status: Msg = Msg.Connecting,
    val reconnecting: Boolean = false,
    val ended: Boolean = false,
    val fatal: PlayerError? = null,
    val audioTracks: List<AudioTrack> = emptyList(),
    val audio: Int = 0,
    val quality: QualityBadge? = null,
    /** plays from TorBox */
    val debrid: Boolean = false,
    /** a cinema recording */
    val cinema: Boolean = false,
    /** "Atmos" / "7.1" / "5.1" of the selected track as it reaches the TV */
    val audioBadge: String = "",
    val toast: Msg? = null,
    /** another release carries the profile language's subtitle that the playing file lacks */
    val subAlt: Boolean = false,
    val countdown: Int? = null,
    /** season to episode of the next episode */
    val next: Pair<Int, Int>? = null,
    val file: PlayingFile? = null,
    /** video width / height (trick play frames) */
    val aspect: Float = 16f / 9f,
) {
    /** A source is open and can be controlled. */
    val playable: Boolean get() = phase == Phase.Ready && duration > 0 && fatal == null
}
