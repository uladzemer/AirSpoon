package io.github.jqssun.airplay.update

internal fun updateBusy(video: Boolean, pendingVideo: Boolean, mirror: Boolean, audioOnly: Boolean, connections: Int): Boolean =
    video || pendingVideo || mirror || audioOnly || connections > 0
