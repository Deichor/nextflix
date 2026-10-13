package com.nextflix.tv.feature.account.intro

import kotlin.math.PI
import kotlin.math.ceil
import kotlin.math.cos
import kotlin.math.pow
import kotlin.math.sin
import kotlin.math.sqrt
import kotlin.random.Random

/**
 * The two-hit sting of the intro, rendered offline (webOS lib/intro.ts builds the same graph with Web Audio):
 * a low thump on each hit, a warm open-fifth swell after the second, a noise reverb and a master fade.
 * Peak is about -12 dBFS. The intro's t = 0 is the first frame.
 */
internal object StingSynth {
    private const val MASTER = 0.2f
    private const val WET = 0.28f
    private const val TAIL = 0.05f

    /** Interleaved stereo 16-bit PCM. */
    fun renderPcm(sampleRate: Int, random: Random = Random.Default): ShortArray {
        val stereo = render(sampleRate, random)
        return ShortArray(stereo.size) { (stereo[it].coerceIn(-1f, 1f) * 32767f).toInt().toShort() }
    }

    /** Interleaved stereo float samples, [Intro.SOUND_END] plus a short tail. */
    fun render(sampleRate: Int, random: Random = Random.Default): FloatArray {
        val sr = sampleRate.toFloat()
        val frames = ceil((Intro.SOUND_END + TAIL) * sr).toInt()
        val bus = FloatArray(frames)
        hit(bus, sr, Intro.HIT1, 46f, 0.55f, 0.85f, random)
        hit(bus, sr, Intro.HIT2, 55f, 1.0f, 1f, random)
        swell(bus, sr, Intro.HIT2 + 0.02f, 1.35f, 0.5f)

        val (left, right) = reverb(bus, sampleRate, random)
        val out = FloatArray(frames * 2)
        val fadeFrom = Intro.SOUND_END - 0.25f
        for (i in 0 until frames) {
            val t = i / sr
            val master = when {
                t < fadeFrom -> MASTER
                t < Intro.SOUND_END -> MASTER * (Intro.SOUND_END - t) / 0.25f
                else -> 0f
            }
            out[2 * i] = (bus[i] + WET * left[i]) * master
            out[2 * i + 1] = (bus[i] + WET * right[i]) * master
        }
        return out
    }

    /** Low thump with a pitch drop, a body an octave up and a soft filtered noise click. */
    private fun hit(bus: FloatArray, sr: Float, t: Float, f: Float, len: Float, level: Float, random: Random) {
        tone(bus, sr, Wave.Sine, t, f * 2.4f, f, 0.12f, level, len)
        tone(bus, sr, Wave.Triangle, t, f * 4f, f * 2f, 0.08f, level * 0.32f, len * 0.55f)
        val start = (t * sr).toInt()
        val count = (0.12f * sr).toInt()
        val lp = Biquad()
        lp.lowpass(2200f / sr, 1f)
        for (n in 0 until count) {
            val i = start + n
            if (i >= bus.size) break
            val x = random.nextFloat() * 2f - 1f
            bus[i] += lp.process(x) * envelope(n / sr, level * 0.22f, 0.002f, 0.09f)
        }
    }

    /** Warm open-fifth pad that swells after the second hit, its filter opening and closing. */
    private fun swell(bus: FloatArray, sr: Float, t: Float, len: Float, level: Float) {
        val notes = floatArrayOf(73.42f, 110f, 146.83f, 220f) // D2, A2, D3, A3
        val start = (t * sr).toInt()
        val count = ((len + 0.05f) * sr).toInt()
        val pad = FloatArray(count)
        for ((i, note) in notes.withIndex()) {
            for (cents in intArrayOf(-6, 6)) {
                val freq = note * 2f.pow(cents / 1200f)
                val wave = if (i < 2) Wave.Triangle else Wave.Saw
                val gain = if (i < 2) 0.3f else 0.1f
                var phase = 0f
                val dt = freq / sr
                for (n in 0 until count) {
                    pad[n] += wave.sample(phase, dt) * gain
                    phase += dt
                    if (phase >= 1f) phase -= 1f
                }
            }
        }
        val lp = Biquad()
        for (n in 0 until count) {
            val i = start + n
            if (i >= bus.size) break
            val local = n / sr
            if (n % 16 == 0) {
                val cutoff = if (local < 0.35f) expRamp(220f, 1500f, local / 0.35f) else expRamp(1500f, 300f, (local - 0.35f) / (len - 0.35f))
                lp.lowpass(cutoff / sr, 0.7f)
            }
            val gain = if (local < 0.28f) expRamp(0.0001f, level, local / 0.28f) else expRamp(level, 0.0001f, (local - 0.28f) / (len - 0.28f))
            bus[i] += lp.process(pad[n]) * gain
        }
    }

    private fun tone(bus: FloatArray, sr: Float, wave: Wave, t: Float, f0: Float, f1: Float, drop: Float, level: Float, len: Float) {
        val start = (t * sr).toInt()
        val count = ((len + 0.05f) * sr).toInt()
        var phase = 0f
        for (n in 0 until count) {
            val i = start + n
            if (i >= bus.size) break
            val local = n / sr
            val freq = if (local < drop) expRamp(f0, f1, local / drop) else f1
            val dt = freq / sr
            bus[i] += wave.sample(phase, dt) * envelope(local, level, 0.006f, len)
            phase += dt
            if (phase >= 1f) phase -= 1f
        }
    }

    /** Exponential envelope: fast attack, decay to silence by [len]. */
    private fun envelope(local: Float, level: Float, attack: Float, len: Float): Float = when {
        local < attack -> expRamp(0.0001f, level, local / attack)
        local < len -> expRamp(level, 0.0001f, (local - attack) / (len - attack))
        else -> 0.0001f
    }

    private fun expRamp(from: Float, to: Float, f: Float): Float = from * (to / from).pow(f.coerceIn(0f, 1f))

    /**
     * Convolution with a decaying stereo noise impulse (1.1 s), scaled like a Web Audio ConvolverNode with
     * normalize = true. One FFT carries both channels: IR_L + i * IR_R.
     */
    private fun reverb(bus: FloatArray, sampleRate: Int, random: Random): Pair<FloatArray, FloatArray> {
        val irLen = (sampleRate * 1.1f).toInt()
        var size = 1
        while (size < bus.size + irLen) size = size shl 1
        val zr = FloatArray(size)
        val zi = FloatArray(size)
        var power = 0.0
        for (i in 0 until irLen) {
            val env = (1f - i.toFloat() / irLen).pow(3.2f)
            zr[i] = (random.nextFloat() * 2f - 1f) * env
            zi[i] = (random.nextFloat() * 2f - 1f) * env
            power += zr[i] * zr[i] + zi[i] * zi[i]
        }
        val rms = sqrt(power / (2.0 * irLen)).coerceAtLeast(0.000125)
        val scale = (10.0.pow(-58.0 * 0.05) / rms * 44100.0 / sampleRate).toFloat()

        val xr = bus.copyOf(size)
        val xi = FloatArray(size)
        Fft.transform(xr, xi, inverse = false)
        Fft.transform(zr, zi, inverse = false)
        for (k in 0 until size) {
            val re = xr[k] * zr[k] - xi[k] * zi[k]
            val im = xr[k] * zi[k] + xi[k] * zr[k]
            zr[k] = re
            zi[k] = im
        }
        Fft.transform(zr, zi, inverse = true)
        val left = FloatArray(bus.size) { zr[it] * scale }
        val right = FloatArray(bus.size) { zi[it] * scale }
        return left to right
    }

    private enum class Wave {
        Sine, Triangle, Saw;

        /** [phase] in cycles (0..1); Web Audio's shapes start at 0 and rise. */
        fun sample(phase: Float, dt: Float): Float = when (this) {
            Sine -> sin(2.0 * PI * phase).toFloat()
            Triangle -> when {
                phase < 0.25f -> 4f * phase
                phase < 0.75f -> 2f - 4f * phase
                else -> 4f * phase - 4f
            }
            Saw -> {
                var q = phase + 0.5f
                if (q >= 1f) q -= 1f
                2f * q - 1f - polyBlep(q, dt)
            }
        }

        private fun polyBlep(q: Float, dt: Float): Float = when {
            q < dt -> {
                val x = q / dt
                x + x - x * x - 1f
            }
            q > 1f - dt -> {
                val x = (q - 1f) / dt
                x * x + x + x + 1f
            }
            else -> 0f
        }
    }

    /** RBJ low-pass; [q] in dB like Web Audio's BiquadFilterNode. */
    private class Biquad {
        private var b0 = 1f
        private var b1 = 0f
        private var b2 = 0f
        private var a1 = 0f
        private var a2 = 0f
        private var x1 = 0f
        private var x2 = 0f
        private var y1 = 0f
        private var y2 = 0f

        /** [cutoff] as a fraction of the sample rate. */
        fun lowpass(cutoff: Float, q: Float) {
            val w0 = 2.0 * PI * cutoff.coerceIn(0.0001f, 0.49f)
            val alpha = sin(w0) / (2.0 * 10.0.pow(q / 20.0))
            val cosw = cos(w0)
            val a0 = 1.0 + alpha
            b0 = ((1.0 - cosw) / 2.0 / a0).toFloat()
            b1 = ((1.0 - cosw) / a0).toFloat()
            b2 = b0
            a1 = (-2.0 * cosw / a0).toFloat()
            a2 = ((1.0 - alpha) / a0).toFloat()
        }

        fun process(x: Float): Float {
            val y = b0 * x + b1 * x1 + b2 * x2 - a1 * y1 - a2 * y2
            x2 = x1
            x1 = x
            y2 = y1
            y1 = y
            return y
        }
    }
}

/** In-place iterative radix-2 FFT; the inverse is scaled by 1/n. */
internal object Fft {
    fun transform(re: FloatArray, im: FloatArray, inverse: Boolean) {
        val n = re.size
        require(n == im.size && n and (n - 1) == 0) { "size must be a power of two" }
        var j = 0
        for (i in 1 until n) {
            var bit = n shr 1
            while (j and bit != 0) {
                j = j xor bit
                bit = bit shr 1
            }
            j = j xor bit
            if (i < j) {
                var t = re[i]; re[i] = re[j]; re[j] = t
                t = im[i]; im[i] = im[j]; im[j] = t
            }
        }
        var len = 2
        while (len <= n) {
            val angle = 2.0 * PI / len * if (inverse) 1 else -1
            val wr = cos(angle)
            val wi = sin(angle)
            val half = len / 2
            var start = 0
            while (start < n) {
                var cr = 1.0
                var ci = 0.0
                for (k in 0 until half) {
                    val a = start + k
                    val b = a + half
                    val tr = (re[b] * cr - im[b] * ci).toFloat()
                    val ti = (re[b] * ci + im[b] * cr).toFloat()
                    re[b] = re[a] - tr
                    im[b] = im[a] - ti
                    re[a] += tr
                    im[a] += ti
                    val next = cr * wr - ci * wi
                    ci = cr * wi + ci * wr
                    cr = next
                }
                start += len
            }
            len = len shl 1
        }
        if (inverse) {
            val s = 1f / n
            for (i in 0 until n) {
                re[i] *= s
                im[i] *= s
            }
        }
    }
}
