package com.nextflix.tv.feature.account

import android.content.Context
import android.content.pm.PackageManager
import android.hardware.display.DisplayManager
import android.os.Build
import android.view.Display
import com.nextflix.tv.core.stream.PlaybackCaps

// The info block under the settings (webOS: model, webOS version, UHD; what plays without conversion; version).

internal fun deviceLine(context: Context): String {
    val maker = Build.MANUFACTURER.orEmpty().replaceFirstChar { it.uppercase() }
    val model = Build.MODEL.orEmpty()
    val name = if (model.startsWith(maker, ignoreCase = true)) model else "$maker $model".trim()
    val display = context.getSystemService(DisplayManager::class.java)?.getDisplay(Display.DEFAULT_DISPLAY)
    val uhd = display?.supportedModes?.any { maxOf(it.physicalWidth, it.physicalHeight) >= 3840 } == true
    return listOfNotNull(name.ifEmpty { null }, "Android ${Build.VERSION.RELEASE}", if (uhd) "UHD" else null).joinToString(" · ")
}

internal fun capsLine(caps: PlaybackCaps): String {
    val video = listOfNotNull(
        if (caps.hevc) "HEVC" else "H.264",
        "HDR10".takeIf { caps.hdr },
        "Dolby Vision".takeIf { caps.dv },
        "Dolby Atmos".takeIf { caps.atmos },
    )
    return "${video.joinToString(", ")} · ${caps.audio.joinToString(", ") { it.uppercase() }}"
}

internal fun appVersion(context: Context): String = try {
    val info = if (Build.VERSION.SDK_INT >= 33) {
        context.packageManager.getPackageInfo(context.packageName, PackageManager.PackageInfoFlags.of(0))
    } else {
        @Suppress("DEPRECATION")
        context.packageManager.getPackageInfo(context.packageName, 0)
    }
    info.versionName.orEmpty()
} catch (_: PackageManager.NameNotFoundException) {
    ""
}
