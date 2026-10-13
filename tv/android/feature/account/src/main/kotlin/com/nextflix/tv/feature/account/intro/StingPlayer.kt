package com.nextflix.tv.feature.account.intro

import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioManager
import android.media.AudioTrack
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/** Plays the rendered sting once (static AudioTrack) and releases it; [stop] fades it out early. */
internal class StingPlayer(private val scope: CoroutineScope) {
    private var track: AudioTrack? = null
    private var releaseJob: Job? = null

    /** Starts the sting as if the intro began [elapsed] seconds ago. Silent if the device refuses the track. */
    @Synchronized
    fun play(pcm: ShortArray, sampleRate: Int, elapsed: Double = 0.0) {
        if (track != null) return
        val frames = pcm.size / 2
        val offset = (elapsed * sampleRate).toInt().coerceAtLeast(0)
        if (offset >= frames) return
        val t = try {
            AudioTrack.Builder()
                .setAudioAttributes(
                    AudioAttributes.Builder()
                        .setUsage(AudioAttributes.USAGE_MEDIA)
                        .setContentType(AudioAttributes.CONTENT_TYPE_MUSIC)
                        .build(),
                )
                .setAudioFormat(
                    AudioFormat.Builder()
                        .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                        .setSampleRate(sampleRate)
                        .setChannelMask(AudioFormat.CHANNEL_OUT_STEREO)
                        .build(),
                )
                .setTransferMode(AudioTrack.MODE_STATIC)
                .setBufferSizeInBytes(pcm.size * 2)
                .build()
        } catch (_: Exception) {
            return
        }
        if (t.state != AudioTrack.STATE_INITIALIZED || t.write(pcm, 0, pcm.size) < 0) {
            t.release()
            return
        }
        if (offset > 0) t.setPlaybackHeadPosition(offset)
        try {
            t.play()
        } catch (_: IllegalStateException) {
            t.release()
            return
        }
        track = t
        val remainingMs = (frames - offset) * 1000L / sampleRate
        releaseJob = scope.launch {
            delay(remainingMs + 200)
            release()
        }
    }

    /** Fades out over [fadeMs], then releases. */
    fun stop(fadeMs: Long = Intro.SKIP_MS) {
        val t = synchronized(this) { track } ?: return
        scope.launch(Dispatchers.Default) {
            val steps = 8
            for (i in steps - 1 downTo 0) {
                synchronized(this@StingPlayer) { if (track === t) t.setVolume(i / steps.toFloat()) }
                delay(fadeMs / steps)
            }
            release()
        }
    }

    @Synchronized
    fun release() {
        releaseJob?.cancel()
        releaseJob = null
        track?.let {
            runCatching { it.stop() }
            it.release()
        }
        track = null
    }

    companion object {
        /** The output's native rate, so the mixer does not resample. */
        fun outputSampleRate(): Int =
            AudioTrack.getNativeOutputSampleRate(AudioManager.STREAM_MUSIC).takeIf { it in 8_000..192_000 } ?: 48_000
    }
}
