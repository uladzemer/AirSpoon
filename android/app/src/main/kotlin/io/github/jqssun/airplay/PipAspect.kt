package io.github.jqssun.airplay

internal fun pipAspectNumerator(aspect: Float): Int =
    ((if (aspect.isNaN()) 16f / 9f else aspect) * 1000).toInt().coerceIn(419, 2390)
