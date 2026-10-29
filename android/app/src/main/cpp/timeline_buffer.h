#ifndef TIMELINE_BUFFER_H
#define TIMELINE_BUFFER_H

#include <algorithm>
#include <atomic>
#include <cstdint>
#include <cstdio>
#include <cstring>
#include <memory>
#include <string>
#include <vector>

#include "audio_time.h"

/*
 * lock-free SPSC ring of int16 samples (interleaved frames); positions are
 * 64-bit counters so empty/full are unambiguous without spare slot. size_t wraps
 * after hours on armv7, breaking counter % capacity for non-power-of-two capacity.
 * Require lock-free atomics: read() runs on the real-time Oboe callback thread.
 */
class SpscRing {
    static_assert(std::atomic<uint64_t>::is_always_lock_free,
                  "SpscRing needs lock-free 64-bit counters");
public:
    explicit SpscRing(size_t capacitySamples)
        : mCapacity(capacitySamples), mBuf(new int16_t[capacitySamples]) {}

    size_t available() const {  // samples readable
        return mWrite.load(std::memory_order_acquire) - mRead.load(std::memory_order_acquire);
    }

    // producer: push up to n samples, drops what doesn't fit
    size_t write(const int16_t *src, size_t n) {
        const uint64_t w = mWrite.load(std::memory_order_relaxed);
        const uint64_t r = mRead.load(std::memory_order_acquire);
        const size_t freeSpace = mCapacity - (w - r);
        if (n > freeSpace) n = freeSpace;
        const size_t wi = w % mCapacity;
        const size_t first = (n < mCapacity - wi) ? n : (mCapacity - wi);
        memcpy(&mBuf[wi], src, first * sizeof(int16_t));
        if (n > first) memcpy(&mBuf[0], src + first, (n - first) * sizeof(int16_t));
        mWrite.store(w + n, std::memory_order_release);
        return n;
    }

    // consumer: pop up to n samples
    size_t read(int16_t *dst, size_t n) {
        const uint64_t r = mRead.load(std::memory_order_relaxed);
        const uint64_t w = mWrite.load(std::memory_order_acquire);
        const size_t avail = w - r;
        if (n > avail) n = avail;
        const size_t ri = r % mCapacity;
        const size_t first = (n < mCapacity - ri) ? n : (mCapacity - ri);
        memcpy(dst, &mBuf[ri], first * sizeof(int16_t));
        if (n > first) memcpy(dst + first, &mBuf[0], (n - first) * sizeof(int16_t));
        mRead.store(r + n, std::memory_order_release);
        return n;
    }

    // consumer: discard up to n oldest samples
    void skip(size_t n) {
        const uint64_t r = mRead.load(std::memory_order_relaxed);
        const uint64_t w = mWrite.load(std::memory_order_acquire);
        const size_t avail = w - r;
        if (n > avail) n = avail;
        mRead.store(r + n, std::memory_order_release);
    }

private:
    const size_t mCapacity;
    std::unique_ptr<int16_t[]> mBuf;
    std::atomic<uint64_t> mWrite{0};
    std::atomic<uint64_t> mRead{0};
};

/*
 * adaptive jitter cushion tuner, NetEQ-inspired but simplified: decaying histogram of
 * packet jitter, cushion = high percentile of it + 1 packet of margin.
 * observe() is producer-thread only; result published via atomic, consumer reads it live
 */
class DelayTracker {
public:
    // staticCushionMs > 0: fixed cushion, tuner disabled
    // staticCushionMs <= 0: tune between MIN_CUSHION_MS and MAX_CUSHION_MS
    DelayTracker(int sampleRate, int channels, int staticCushionMs, int percentilePct)
        : mSampleRate(sampleRate), mChannels(channels),
          mAdaptive(staticCushionMs <= 0),
          mFloor(msToSamples(sampleRate, channels, mAdaptive ? MIN_CUSHION_MS : staticCushionMs)),
          mCeil(msToSamples(sampleRate, channels, mAdaptive ? MAX_CUSHION_MS : staticCushionMs)),
          mPercentilePct(percentilePct),
          mTuned(mFloor) {}

    // ptsNs: packet presentation time; arrivalNs: local arrival time; durNs: packet duration
    void observe(int64_t ptsNs, int64_t arrivalNs, int64_t durNs) {
        if (!mAdaptive) return;

        const int64_t raw = arrivalNs - ptsNs; // arrival latency
        // jitter = latency relative to min latency seen so far (mBaseNs)
        if (!mHaveBase) {
            mBaseNs = raw; mHaveBase = true;
            mSettleUntilNs.store(arrivalNs + STALL_SETTLE_NS, std::memory_order_relaxed);
        }
        else if (raw < mBaseNs) mBaseNs = raw;
        // creep mBaseNs up slowly: counters clock drift and one freak fast packet
        else mBaseNs += (raw - mBaseNs) >> BASE_CREEP_SHIFT;
        int64_t rel = raw - mBaseNs;
        if (rel < 0) rel = 0;

        int bin = (int)(rel / BUCKET_NS);
        if (bin >= NBUCKETS) bin = NBUCKETS - 1;

        // decay: each bin bleeds 1/2^FORGET_SHIFT per packet, current bin topped up by
        // ONE/2^FORGET_SHIFT, so total converges to ONE
        int64_t sum = 0;
        for (int i = 0; i < NBUCKETS; i++) {
            mHist[i] -= (mHist[i] + (1 << FORGET_SHIFT) - 1) >> FORGET_SHIFT;
            sum += mHist[i];
        }
        mHist[bin] += ONE >> FORGET_SHIFT;
        sum += ONE >> FORGET_SHIFT;

        // cushion = smallest delay whose cumulative histogram share reaches percentile
        const int64_t thresh = sum * mPercentilePct / 100;
        int64_t cum = 0; int b = 0;
        for (; b < NBUCKETS - 1; b++) { cum += mHist[b]; if (cum >= thresh) break; }
        // bin is lower bound on delay: take top edge + margin. margin must keep output
        // fed for one data callback and tide us over until next packet
        const int64_t marginNs = std::max(durNs, mOutputBufferNs.load(std::memory_order_relaxed));
        const int64_t targetNs = (int64_t)(b + 1) * BUCKET_NS + marginNs;
        size_t samples = (size_t)(targetNs * mSampleRate / NS_PER_SEC) * mChannels;
        if (samples < mFloor) samples = mFloor;
        if (samples > mCeil) samples = mCeil;
        mTuned.store(samples, std::memory_order_relaxed);

        // AirSpoon: packet far later than the p95 cushion = Wi-Fi stall, remember it
        if (arrivalNs >= mSettleUntilNs.load(std::memory_order_relaxed) &&
            rel > STALL_MIN_LATE_NS && rel > (int64_t)(samples / mChannels) * NS_PER_SEC / mSampleRate) {
            raiseFloor(rel, RAISE_LATE, arrivalNs);
        }
    }

    // AirSpoon: consumer underran; observedNs = how long writes were absent (stall length).
    // producer-thread only, same as observe()
    void noteUnderrun(int64_t observedNs, int64_t nowNs) { raiseFloor(observedNs, RAISE_UNDERRUN, nowNs); }

    // AirSpoon: pending "floor raised" notification for the log (producer-thread only)
    struct Raise { int cause; int64_t observedNs, floorNs; };
    bool takeRaise(Raise &out) {
        if (!mRaisePending) return false;
        out = mRaise;
        mRaisePending = false;
        return true;
    }
    enum { RAISE_LATE = 0, RAISE_UNDERRUN = 1 };

    // output restarted / buffer flushed: latency base needs time to settle again; any thread
    void noteRestart() { mSettleUntilNs.store(monoNs() + STALL_SETTLE_NS, std::memory_order_relaxed); }

    // current stall floor in ms (stats line); any thread
    int floorMs() const { return (int)(floorNs(monoNs()) / 1'000'000LL); }

    // reset clock base after discontinuity or clock shift/resync so it doesn't poison
    // jitter calculations; producer-thread only (same as observe())
    void reanchor() { mHaveBase = false; }

    // latest tuned cushion in samples; any thread
    // AirSpoon: tuned = max(p95-based value, stall-memory floor)
    size_t target() const {
        const size_t t = mTuned.load(std::memory_order_relaxed);
        const size_t f = floorSamples(monoNs());
        return f > t ? f : t;
    }

    void noteOutputBufferFrames(int frames) {
        mOutputBufferNs.store((int64_t)frames * NS_PER_SEC / mSampleRate, std::memory_order_relaxed);
    }

    // largest cushion tuner can reach, in samples
    size_t ceil() const { return mCeil; }

private:
    static size_t msToSamples(int sampleRate, int channels, int ms) {
        return (size_t)sampleRate * ms / 1000 * channels;
    }

    // ---- stall-memory floor (AirSpoon). All tunables in one place. ----
    // A stall (packet late by more than STALL_MIN_LATE_MS and more than the p95 cushion, or an
    // underrun) raises a peak-hold floor = observed lateness + STALL_MARGIN_MS + one output
    // buffer, capped at STALL_FLOOR_MAX_MS. Floor holds STALL_HOLD_NS, then decays linearly to 0
    // over STALL_DECAY_NS; a new stall of comparable size (>= STALL_REARM_PCT of the current floor)
    // re-arms it, smaller lateness does not extend it. Stalls longer than STALL_IGNORE_ABOVE_MS
    // are sender pauses / session gaps, not Wi-Fi stalls, and are ignored (late and underrun
    // paths alike). Late-path raises are also ignored for STALL_SETTLE_NS after tracker
    // (re)anchor or flush: the latency base is not settled yet. Underruns only count when audio
    // had been playing (see TimelineBuffer::STALL_MIN_PLAYED_MS). Floor is wall-clock based
    // and NOT cleared on flushAndReprime(): the iPad tears audio down every ~30 s and the floor
    // must survive that to cover the next stall. A new TimelineBuffer (engine reconfigure)
    // starts at 0.
    static constexpr int STALL_MIN_LATE_MS = 50;
    static constexpr int STALL_MARGIN_MS = 30;
    static constexpr int STALL_FLOOR_MAX_MS = 400;
    static constexpr int STALL_IGNORE_ABOVE_MS = 500;   // observed Wi-Fi stalls are ~250-270 ms
    static constexpr int STALL_REARM_PCT = 60;          // re-arm hold only for a stall >= 60% of current floor
    static constexpr int64_t STALL_SETTLE_NS = 3'000'000'000LL;  // ignore late-path raises this long after (re)start
    static constexpr int64_t STALL_HOLD_NS = 60'000'000'000LL;
    static constexpr int64_t STALL_DECAY_NS = 60'000'000'000LL;
    static constexpr int64_t STALL_MIN_LATE_NS = (int64_t)STALL_MIN_LATE_MS * 1'000'000LL;

    // floor at nowNs in ns: peak while holding, then linear decay
    int64_t floorNs(int64_t nowNs) const {
        // peak and armed time come from one atomic word, so they are always a consistent pair
        const uint64_t w = mStallFloor.load(std::memory_order_relaxed);
        const int64_t peak = (int64_t)(w >> 48) * 1'000'000LL;
        if (peak <= 0) return 0;
        const int64_t age = nowNs - (int64_t)(w & FLOOR_ARMED_MASK) * 1'000'000LL;
        const int64_t cap = (int64_t)STALL_FLOOR_MAX_MS * 1'000'000LL;
        const int64_t top = peak > cap ? cap : peak;
        if (age <= STALL_HOLD_NS) return top;
        if (age >= STALL_HOLD_NS + STALL_DECAY_NS) return 0;
        // double: peak (~4e8) * decay age (~6e10) overflows int64
        const double frac = (double)(age - STALL_HOLD_NS) / (double)STALL_DECAY_NS;
        const int64_t v = (int64_t)((double)top * (1.0 - frac));
        return v < 0 ? 0 : (v > top ? top : v);
    }
    size_t floorSamples(int64_t nowNs) const {
        const size_t s = (size_t)(floorNs(nowNs) * mSampleRate / NS_PER_SEC) * mChannels;
        return s > mCeil ? mCeil : s;
    }

    // pack: peak in ms (16 bits, rounded up, max 400) | armed time in ms (48 bits)
    static constexpr uint64_t FLOOR_ARMED_MASK = (1ULL << 48) - 1;
    void storeFloor(int64_t peakNs, int64_t nowNs) {
        const uint64_t peakMs = (uint64_t)((peakNs + 999'999) / 1'000'000LL);
        mStallFloor.store((peakMs << 48) | ((uint64_t)(nowNs / 1'000'000LL) & FLOOR_ARMED_MASK),
                     std::memory_order_relaxed);
    }

    // producer thread
    void raiseFloor(int64_t observedNs, int cause, int64_t nowNs) {
        if (!mAdaptive || observedNs <= 0 || observedNs > (int64_t)STALL_IGNORE_ABOVE_MS * 1'000'000LL) return;
        int64_t want = observedNs + (int64_t)STALL_MARGIN_MS * 1'000'000LL +
                       mOutputBufferNs.load(std::memory_order_relaxed);
        const int64_t cap = (int64_t)STALL_FLOOR_MAX_MS * 1'000'000LL;
        if (want > cap) want = cap;
        const int64_t cur = floorNs(nowNs);
        if (want > cur) {
            // keep the larger of two raises within one write
            if (!mRaisePending || want > mRaise.floorNs) mRaise = {cause, observedNs, want};
            mRaisePending = true;
            storeFloor(want, nowNs);
        } else if (want * 100 >= cur * STALL_REARM_PCT) {
            // comparable stall: hold the current level for a full hold again
            storeFloor(cur, nowNs);
        }
        // else: smaller lateness, floor keeps decaying undisturbed
    }

    static constexpr int MIN_CUSHION_MS = 0;             // algorithm already enforces effective floor
    static constexpr int MAX_CUSHION_MS = 1000;
    static constexpr int BUCKET_MS = 5;                  // histogram granularity
    static constexpr int NBUCKETS = MAX_CUSHION_MS / BUCKET_MS;
    static constexpr int64_t BUCKET_NS = (int64_t)BUCKET_MS * 1000000LL;
    static constexpr int FORGET_SHIFT = 8;               // ~1/256 decay per packet (~6s @ 23ms)
    static constexpr int BASE_CREEP_SHIFT = 10;          // mBaseNs creep speed
    static constexpr int64_t ONE = 1 << 20;              // histogram fixed-point unit

    const int mSampleRate;
    const int mChannels;
    const bool mAdaptive;                // false = fixed cushion (tuner disabled)
    const size_t mFloor;
    const size_t mCeil;
    const int mPercentilePct;            // arrival-delay percentile cushion targets
    std::atomic<size_t> mTuned;
    std::atomic<int64_t> mOutputBufferNs{0};  // output->producer: oboe output buffer duration
    int64_t mHist[NBUCKETS] = {};
    int64_t mBaseNs = 0;
    bool mHaveBase = false;
    std::atomic<uint64_t> mStallFloor{0};      // stall floor: peakMs<<48 | armedMs; peak 0 = none
    std::atomic<int64_t> mSettleUntilNs{0}; // late-path raises ignored before this time
    Raise mRaise{};                         // producer-only
    bool mRaisePending = false;             // producer-only
};

// AirSpoon: диагностические события для лога. Пишутся из реального времени (колбэк Oboe)
// или из потока записи без блокировок и аллокаций, выводятся в лог из потока RAOP
// (TimelineBuffer::drainEvents). SPSC; при переполнении событие теряется и считается.
struct TimelineEvent {
    int64_t tNs;         // момент события, CLOCK_MONOTONIC
    int32_t type;
    int64_t a, b, c, d;  // поля по типу события, время в микросекундах
};

template <size_t N>
class EventRing {
public:
    void push(const TimelineEvent &e) {
        const uint32_t w = mW.load(std::memory_order_relaxed);
        const uint32_t r = mR.load(std::memory_order_acquire);
        if (w - r >= N) { mDropped.fetch_add(1, std::memory_order_relaxed); return; }
        mBuf[w % N] = e;
        mW.store(w + 1, std::memory_order_release);
    }
    bool pop(TimelineEvent &e) {
        const uint32_t r = mR.load(std::memory_order_relaxed);
        if (r == mW.load(std::memory_order_acquire)) return false;
        e = mBuf[r % N];
        mR.store(r + 1, std::memory_order_release);
        return true;
    }
    uint32_t takeDropped() { return mDropped.exchange(0, std::memory_order_relaxed); }
private:
    TimelineEvent mBuf[N];
    std::atomic<uint32_t> mW{0}, mR{0}, mDropped{0};
};

// cumulative diagnostic counters, bumped from producer/consumer, read from stats thread
class TimelineMetrics {
public:
    // packed: nests into AudioDebugData with fixed layout
    struct __attribute__((packed)) Debug {
        uint32_t trims, drops, silences, underruns;  // cumulative; 32-bit to avoid wrap
    };

    void countTrim()     { mTrims.fetch_add(1, std::memory_order_relaxed); }
    void countDrop()     { mDrops.fetch_add(1, std::memory_order_relaxed); }
    void countSilence()  { mSilences.fetch_add(1, std::memory_order_relaxed); }
    void countUnderrun() { mUnderruns.fetch_add(1, std::memory_order_relaxed); }

    // any thread
    Debug debugInfo() const {
        Debug d{};
        d.trims = mTrims.load(std::memory_order_relaxed);
        d.drops = mDrops.load(std::memory_order_relaxed);
        d.silences = mSilences.load(std::memory_order_relaxed);
        d.underruns = mUnderruns.load(std::memory_order_relaxed);
        return d;
    }

private:
    std::atomic<uint32_t> mTrims{0};      // backlog exceeded cap and was trimmed
    std::atomic<uint32_t> mDrops{0};      // late packets dropped, pts slot already passed
    std::atomic<uint32_t> mSilences{0};   // gaps filled with silence
    std::atomic<uint32_t> mUnderruns{0};  // ring ran dry, output padded with silence
};

/*
 * Timeline-synchronized playout buffer: absorbs jitter, clock drift and clock resyncs,
 * syncs audio to its presentation-time tag.
 *
 * One concurrent reader + one concurrent writer OK; multiple readers or writers NOT safe.
 * samples = frames * channels
 */
class TimelineBuffer {
public:
    // cushion policy lives in mTracker; timeline reads mTracker.target() live
    TimelineBuffer(int sampleRate, int channels, int staticCushionMs, int percentilePct)
        : mSampleRate(sampleRate),
          mChannels(channels),
          mTracker(sampleRate, channels, staticCushionMs, percentilePct),
          // ring can't be reallocated mid-stream: size for worst-case backlog, i.e. cap
          // of largest reachable cushion + one throttle window of overshoot (trims are
          // throttled), 2x slack
          mRing(2 * capOf(mTracker.ceil()) + (size_t)sampleRate * TRIM_THROTTLE_MS / 1000 * channels),
          mLastOut(new int16_t[channels]()) {
        mPrev.resize((size_t)2048 * channels);  // fits any AAC/ALAC frame: write() normally never allocates
    }

    void noteOutputBufferFrames(int frames) { mTracker.noteOutputBufferFrames(frames); }

    // producer: push samples
    void write(const int16_t *pcm, size_t samples, int64_t ptsNs) {
        if (samples == 0) return;
        const int64_t nowW = monoNs();
        const int64_t prevWriteNs = mLastWriteNs.exchange(nowW, std::memory_order_relaxed);
        // pts 0 = sender clock not NTP synced yet
        if (ptsNs == 0) {
            // an underrun flag may still be pending (its re-anchor is kept for the next timed
            // write), but its stall duration is stale now: do not let it feed the floor
            mUnderranStall.store(false, std::memory_order_relaxed);
            mRing.write(pcm, samples);
            return;
        }
        const int64_t durNs = samples * NS_PER_SEC / mChannels / mSampleRate;

        // consumer underran since last write: it already played the gap as real-time
        // silence, re-anchor so we don't also insert it here
        if (mUnderran.exchange(false, std::memory_order_acquire)) {
            mExpectedPtsNs = ptsNs;
            // AirSpoon: stall length = silence between writes; feeds the cushion floor
            // only if audio had actually been playing before it (not a restart/priming gap)
            if (prevWriteNs && mUnderranStall.exchange(false, std::memory_order_relaxed))
                mTracker.noteUnderrun(nowW - prevWriteNs, nowW);
        }
        const int64_t gapNs = ptsNs - mExpectedPtsNs;

        // large gap: possible discontinuity or clock shift. reanchor() must precede
        // observe() so the reset applies to this observation
        if (gapNs > MAX_GAP_NS || gapNs < -MAX_GAP_NS) mTracker.reanchor();

        // no-op in static-cushion mode
        mTracker.observe(ptsNs, nowW, durNs);
        DelayTracker::Raise raise;
        while (mTracker.takeRaise(raise))
            mProducerEvents.push(TimelineEvent{nowW, EV_FLOOR, raise.cause, raise.observedNs / 1000,
                                               raise.floorNs / 1000, 0});

        // AirSpoon: после любого разрыва начало нового кадра плавно нарастает из нуля
        bool fadeInNew = false;
        if (gapNs > MAX_GAP_NS || gapNs < -MAX_GAP_NS) {
            // discontinuity: write immediately. new sound after long silence resets
            // backlog to ideal size (less latency); on clock resync we keep the sound
            pushGapEvent(nowW, GAP_DISCONT, gapNs);
            // fade in only if nothing audible precedes it (empty ring or priming): otherwise
            // a clock resync with live audio queued would get a pointless dip
            fadeInNew = mRing.available() == 0 || mPriming.load(std::memory_order_relaxed);
        } else if (gapNs > SLACK_NS) {
            const size_t gapFrames = (size_t)(gapNs * mSampleRate / NS_PER_SEC);
            if (gapNs <= 2 * durNs && mPrevSamples > 0) {
                // AirSpoon: потерян один кадр: вместо нулей предыдущий кадр с затуханием
                writeConcealFrames(gapFrames);
                pushGapEvent(nowW, GAP_CONCEAL, gapNs);
            } else {
                // sender left gap: reproduce as silence so timing is exact
                writeSilenceFrames(gapFrames);
                pushGapEvent(nowW, GAP_SILENCE, gapNs);
            }
            mMetrics.countSilence();
            fadeInNew = true;
        } else if (gapNs < -SLACK_NS) {
            // slot already passed (late/overlap): drop
            mMetrics.countDrop();
            pushGapEvent(nowW, GAP_DROP, gapNs);
            return;
        }
        if (fadeInNew) {
            // нарастание первых кадров из нуля; остальное пишем как есть
            const size_t frames = samples / mChannels;
            const size_t nf = std::min<size_t>(std::min<size_t>(frames, (size_t)FADE_FRAMES),
                                               (size_t)(1024 / mChannels));
            int16_t head[1024];
            for (size_t f = 0; f < nf; f++) {
                const int32_t g = rampGain(f + 1, nf);  // 0 -> 1
                for (int c = 0; c < mChannels; c++)
                    head[f * mChannels + c] = (int16_t)((pcm[f * mChannels + c] * g) >> 15);
            }
            mRing.write(head, nf * mChannels);
            mRing.write(pcm + nf * mChannels, samples - nf * mChannels);
        } else {
            mRing.write(pcm, samples);
        }
        // последний кадр без затухания: заготовка для маскировки потери
        if (samples > mPrev.size()) mPrev.resize(samples);
        memcpy(mPrev.data(), pcm, samples * sizeof(int16_t));
        mPrevSamples = samples;
        mExpectedPtsNs = ptsNs + durNs;
    }

    // consumer: pop frames*channels samples into out, silence-padded on underrun
    void read(int16_t *out, int32_t frames) {
        const size_t need = (size_t)frames * mChannels;
        if (mFlushRequested.exchange(false, std::memory_order_acquire)) {
            flushAndReprime();
            mPrimeSilenceFrames = 0;
            mAboveCapSinceNs = 0;
            mLastTrimBlockNs = 0;
            mFadeIn = true;
            memset(mLastOut.get(), 0, mChannels * sizeof(int16_t));
            memset(out, 0, need * sizeof(int16_t));
            return;
        }
        const size_t tuned = mTracker.target();
        const int64_t now = monoNs();

        // bound latency: cap tracks tuned cushion live so a calmed link sheds latency
        // without an underrun. each trim costs a glitch, so be conservative: trim only
        // after backlog stayed above cap for TRIM_SUSTAIN (skip transient spikes), at
        // most once per TRIM_THROTTLE, and not within that window of an underrun
        // (trimming while rebuilding cushion is counterproductive)
        const size_t avail = mRing.available();
        size_t preFrames = 0;  // frames already placed at the start of out by the trim fade-out
        if (avail > capOf(tuned)) {
            if (mAboveCapSinceNs == 0) mAboveCapSinceNs = now;
        } else {
            mAboveCapSinceNs = 0;
        }
        if (mAboveCapSinceNs != 0 && now - mAboveCapSinceNs >= TRIM_SUSTAIN_NS
                && now - mLastTrimBlockNs >= TRIM_THROTTLE_NS) {
            if (!mPriming) {
                // AirSpoon: take the next TRIM_FADE_FRAMES of audio at the current position
                // straight into out, fade them out, then skip; the rest follows faded in
                const size_t nf = std::min<size_t>((size_t)frames, (size_t)TRIM_FADE_FRAMES);
                const size_t n = mRing.read(out, nf * mChannels);
                preFrames = n / mChannels;
                for (size_t f = 0; f < preFrames; f++) {
                    const int32_t g = 32768 - rampGain(f + 1, preFrames);  // ~1 -> 0
                    for (int c = 0; c < mChannels; c++)
                        out[f * mChannels + c] = (int16_t)((out[f * mChannels + c] * g) >> 15);
                }
                const size_t rest = mRing.available();
                if (rest > tuned) mRing.skip(rest - tuned);
            } else {
                mRing.skip(avail - tuned);
            }
            mFadeIn = true;  // AirSpoon: обрыв потока, плавное начало после пропуска
            mMetrics.countTrim();
            mLastTrimBlockNs = now;
            mAboveCapSinceNs = 0;
        }

        // prebuffer: hold output until cushion fills, builds jitter headroom
        if (mPriming) {
            const size_t buffered = mRing.available();
            // time the wait only while holding partial data; reset on empty ring so the
            // producer gets the full window once a sound starts, otherwise we could time
            // out on pre-sound silence and underrun the instant the first frame lands
            if (buffered == 0) mPrimeSilenceFrames = 0;
            else mPrimeSilenceFrames += (uint32_t)frames;
            // AirSpoon: priming clock starts at the first non-empty read (underrun sets it itself)
            if (buffered > 0 && mPrimeStartNs == 0) mPrimeStartNs = now;
            // short sound can end before filling cushion; after holding partial data
            // through 2x cushion of silence the sound is complete: play it
            const uint32_t starveFrames = (uint32_t)(2 * tuned / mChannels);
            const bool starved = buffered > 0 && mPrimeSilenceFrames >= starveFrames;
            // also keep priming on empty ring: with 0 cushion `buffered < tuned` is
            // never true, so we'd otherwise underrun on every empty read between sounds
            if (buffered == 0 || (buffered < tuned && !starved)) {
                memset(out, 0, need * sizeof(int16_t));
                memset(mLastOut.get(), 0, mChannels * sizeof(int16_t));
                return;
            }
            pushEvent(now, EV_PRIME_EXIT, (now - (mPrimeStartNs ? mPrimeStartNs : now)) / 1000,
                      samplesToUs(buffered), samplesToUs(tuned), 0);
            mPriming = false;
            mPrimeSilenceFrames = 0;
            mPrimeStartNs = 0;
            mPlayedFrames = 0;
            mFadeIn = true;  // AirSpoon: плавное начало после прайминга
        }

        const size_t preSamples = preFrames * mChannels;
        const size_t got = preSamples + mRing.read(out + preSamples, need - preSamples);
        const size_t gotFrames = got / mChannels;
        mPlayedFrames += gotFrames;
        if (mFadeIn && gotFrames > preFrames) {
            mFadeIn = false;
            const size_t nf = std::min<size_t>(gotFrames - preFrames, (size_t)FADE_FRAMES);
            for (size_t f = preFrames; f < preFrames + nf; f++) {
                const int32_t g = rampGain(f - preFrames + 1, nf);  // 0 -> 1
                for (int c = 0; c < mChannels; c++)
                    out[f * mChannels + c] = (int16_t)((out[f * mChannels + c] * g) >> 15);
            }
        }
        if (got < need) {
            // AirSpoon: событие до countUnderrun: момент и состояние на входе в underrun
            const int64_t lastW = mLastWriteNs.load(std::memory_order_relaxed);
            pushEvent(now, EV_UNDERRUN, samplesToUs(got), samplesToUs(need), samplesToUs(tuned),
                      lastW ? (now - lastW) / 1000 : -1);
            // AirSpoon: затухание последних реальных кадров в ноль перед тишиной
            if (gotFrames > 0) {
                const size_t nf = std::min<size_t>(gotFrames, (size_t)FADE_FRAMES);
                const size_t start = gotFrames - nf;
                for (size_t f = 0; f < nf; f++) {
                    const int32_t g = 32768 - rampGain(f + 1, nf);  // ~1 -> 0
                    for (int c = 0; c < mChannels; c++)
                        out[(start + f) * mChannels + c] =
                            (int16_t)((out[(start + f) * mChannels + c] * g) >> 15);
                }
            }
            memset(out + got, 0, (need - got) * sizeof(int16_t));
            if (gotFrames == 0) {
                // ничего не прочитали: затухание хвоста прошлого колбэка, а не обрыв в ноль
                const size_t nf = std::min<size_t>((size_t)frames, (size_t)FADE_FRAMES);
                for (size_t f = 0; f < nf; f++) {
                    const int32_t g = 32768 - rampGain(f + 1, nf);  // ~1 -> 0
                    for (int c = 0; c < mChannels; c++)
                        out[f * mChannels + c] = (int16_t)((mLastOut[c] * g) >> 15);
                }
            }
            mPriming = true;
            mPrimeStartNs = now;
            mLastTrimBlockNs = now;  // hold off trims while rebuilding
            mUnderranStall.store(mPlayedFrames >= (uint64_t)mSampleRate * STALL_MIN_PLAYED_MS / 1000,
                                 std::memory_order_relaxed);
            mUnderran.store(true, std::memory_order_release);  // producer re-anchors; publishes mUnderranStall
            mMetrics.countUnderrun();
        }
        if (frames > 0)
            memcpy(mLastOut.get(), out + (size_t)(frames - 1) * mChannels, mChannels * sizeof(int16_t));
    }

    // AirSpoon: выводит накопленные диагностические события в лог (поток RAOP, единственный
    // потребитель). budget: сколько строк ещё можно вывести в текущем окне; остальные
    // события уходят в suppressed. Log: любой тип с info(const char *, ...)
    template <class Log>
    // perCall: max lines printed in this call (log emit is slow); the rest stay queued
    void drainEvents(Log &log, int &budget, uint32_t &suppressed, int &perCall) {
        TimelineEvent e;
        for (int ring = 0; ring < 2; ring++) {
            // over-budget events are only counted (cheap), so they are not limited by perCall
            while ((budget <= 0 || perCall > 0) &&
                   (ring == 0 ? mConsumerEvents.pop(e) : mProducerEvents.pop(e))) {
                if (budget <= 0) { suppressed++; continue; }
                budget--;
                perCall--;
                const long long t = (long long)(e.tNs / 1'000'000LL);
                switch (e.type) {
                    case EV_UNDERRUN:
                        log.info("UNDERRUN t=%lld availBefore=%.1f need=%.1f tuned=%.1f sinceLastWrite=%.1f",
                                 t, e.a / 1000.0, e.b / 1000.0, e.c / 1000.0, e.d / 1000.0);
                        break;
                    case EV_PRIME_EXIT:
                        log.info("PRIME_EXIT t=%lld muteMs=%.1f buffered=%.1f tuned=%.1f",
                                 t, e.a / 1000.0, e.b / 1000.0, e.c / 1000.0);
                        break;
                    case EV_FLOOR:
                        log.info("CUSHION_FLOOR t=%lld floorMs=%.1f cause=%s observedMs=%.1f", t,
                                 e.c / 1000.0, e.a == DelayTracker::RAISE_UNDERRUN ? "underrun" : "late",
                                 e.b / 1000.0);
                        break;
                    default: {
                        static const char *const kinds[] = {"silence", "drop", "discont", "conceal"};
                        log.info("GAP t=%lld kind=%s gapMs=%.2f", t,
                                 kinds[(e.a >= 0 && e.a <= GAP_CONCEAL) ? e.a : 0], e.b / 1000.0);
                        break;
                    }
                }
            }
        }
        suppressed += mConsumerEvents.takeDropped() + mProducerEvents.takeDropped();
    }

    // call after discontinuity or clock shift/resync; producer-side, must not run
    // concurrently with write()
    void reanchorTracker() { mTracker.reanchor(); }
    void requestReprime() { mFlushRequested.store(true, std::memory_order_release); }

    // Producer stopped/joined by the engine; read head remains consumer-owned.
    void requestFlush() {
        mExpectedPtsNs = 0;
        mPrevSamples = 0;
        mLastWriteNs.store(0, std::memory_order_relaxed);
        mUnderran.store(false, std::memory_order_relaxed);
        mUnderranStall.store(false, std::memory_order_relaxed);
        mTracker.reanchor();
        mTracker.noteRestart();
        mFlushRequested.store(true, std::memory_order_release);
    }

    // rebuild prebuffer cushion before resuming, e.g. after output stream restart;
    // must not run concurrently with read()
    void reprime() { mPriming = true; mPrimeStartNs = 0; mPlayedFrames = 0; mTracker.noteRestart(); }

    // drop buffered audio + rebuild cushion, so resume after pause doesn't play stale
    // tail; only while output callback is stopped (skip() is consumer-side)
    void flushAndReprime() {
        mRing.skip(mRing.available());
        mPriming = true;
        mPrimeStartNs = 0;
        mPlayedFrames = 0;
        mTracker.noteRestart();
    }

    // debug snapshot: backlog + tuned cushion (ms) + counters; reads only atomics, any thread
    struct __attribute__((packed)) Debug {
        uint16_t backlogMs;         // bounded by ring (few seconds)
        uint16_t tunedCushionMs;    // bounded by MAX_CUSHION_MS
        TimelineMetrics::Debug metrics;
    };
    // AirSpoon: current stall-memory floor in ms (for the stats line)
    int floorMs() const { return mTracker.floorMs(); }
    Debug debugInfo() const {
        Debug d{};
        d.backlogMs = (uint16_t)(mRing.available() / mChannels * 1000 / mSampleRate);
        d.tunedCushionMs = (uint16_t)(mTracker.target() / mChannels * 1000 / mSampleRate);
        d.metrics = mMetrics.debugInfo();
        return d;
    }

private:

    // latency ceiling for cushion: 2x, but never below TRIM_FLOOR_MS so tiny cushion
    // still gets slack before a trim (trim costs a glitch)
    size_t capOf(size_t cushionSamples) const {
        return std::max(cushionSamples * 2, (size_t)mSampleRate * TRIM_FLOOR_MS / 1000 * mChannels);
    }

    enum { EV_UNDERRUN = 1, EV_PRIME_EXIT = 2, EV_GAP = 3, EV_FLOOR = 4 };
    enum { GAP_SILENCE = 0, GAP_DROP = 1, GAP_DISCONT = 2, GAP_CONCEAL = 3 };
    enum { FADE_FRAMES = 128, TRIM_FADE_FRAMES = 64 };  // ~2.9 ms @44.1k

    // линейная рампа Q15: i в 1..n даёт (0, 32768)
    static int32_t rampGain(size_t i, size_t n) { return (int32_t)((i * 32768) / (n + 1)); }

    int64_t samplesToUs(size_t samples) const {
        return (int64_t)(samples / mChannels) * 1'000'000LL / mSampleRate;
    }

    // consumer thread (RT): без блокировок и аллокаций
    void pushEvent(int64_t tNs, int type, int64_t a, int64_t b, int64_t c, int64_t d) {
        mConsumerEvents.push(TimelineEvent{tNs, type, a, b, c, d});
    }
    // producer thread
    void pushGapEvent(int64_t tNs, int kind, int64_t gapNs) {
        mProducerEvents.push(TimelineEvent{tNs, EV_GAP, kind, gapNs / 1000, 0, 0});
    }

    // потеря одного кадра: копия предыдущего кадра, линейно затухающая до нуля, на
    // gapFrames кадров (остаток, если предыдущий короче, дописывается нулями)
    void writeConcealFrames(size_t gapFrames) {
        const size_t prevFrames = mPrevSamples / mChannels;
        const size_t n = std::min(prevFrames, gapFrames);
        int16_t chunk[512];
        const size_t chunkFrames = 512 / mChannels;
        for (size_t f0 = 0; f0 < n; f0 += chunkFrames) {
            const size_t m = std::min(chunkFrames, n - f0);
            for (size_t f = 0; f < m; f++) {
                const int32_t g = 32768 - rampGain(f0 + f + 1, n);  // ~1 -> 0
                for (int c = 0; c < mChannels; c++)
                    chunk[f * mChannels + c] = (int16_t)((mPrev[(f0 + f) * mChannels + c] * g) >> 15);
            }
            mRing.write(chunk, m * mChannels);
        }
        if (gapFrames > n) writeSilenceFrames(gapFrames - n);
    }

    void writeSilenceFrames(size_t frames) {
        int16_t zeros[512] = {0};  // 256 stereo frames per chunk
        size_t samples = frames * mChannels;
        while (samples > 0) {
            const size_t n = std::min<size_t>(samples, 512);
            mRing.write(zeros, n);
            samples -= n;
        }
    }

    static constexpr int64_t SLACK_NS = 10'000'000LL;    // ignore <10ms gaps (NTP shift, rounding)
    static constexpr int64_t MAX_GAP_NS = 750'000'000LL; // >750ms = discontinuity, re-anchor
    static constexpr int STALL_MIN_PLAYED_MS = 500;      // underrun feeds the cushion floor only after this much playback since priming
    static constexpr int TRIM_FLOOR_MS = 30;             // min trim point even for tiny cushion
    static constexpr int TRIM_THROTTLE_MS = 2000;        // min spacing between trims, and after underrun
    static constexpr int64_t TRIM_THROTTLE_NS = (int64_t)TRIM_THROTTLE_MS * 1'000'000LL;
    static constexpr int TRIM_SUSTAIN_MS = 2000;         // backlog must exceed cap this long before trim
    static constexpr int64_t TRIM_SUSTAIN_NS = (int64_t)TRIM_SUSTAIN_MS * 1'000'000LL;

    const int mSampleRate;
    const int mChannels;
    DelayTracker mTracker;               // owns cushion policy; read live each read()
    SpscRing mRing;
    TimelineMetrics mMetrics;
    std::atomic<bool> mPriming{true};    // stream start: buffer output to build cushion
    uint32_t mPrimeSilenceFrames = 0;    // consumer-only: silence frames output while priming with partial data
    int64_t mLastTrimBlockNs = 0;        // consumer-only: last trim/underrun time (trim throttle)
    int64_t mAboveCapSinceNs = 0;        // consumer-only: when backlog first exceeded cap (0 = under)
    std::atomic<bool> mUnderran{false};  // consumer->producer: underrun happened
    std::atomic<bool> mFlushRequested{false};
    std::atomic<bool> mUnderranStall{false};  // consumer->producer: that underrun counts as a Wi-Fi stall
    uint64_t mPlayedFrames = 0;          // consumer-only: frames played since last priming exit

    int64_t mExpectedPtsNs = 0;          // presentation time expected at write head

    // AirSpoon: затухания и диагностика
    std::vector<int16_t> mPrev;            // producer-only: последний записанный кадр (сырой)
    size_t mPrevSamples = 0;               // producer-only
    std::unique_ptr<int16_t[]> mLastOut;   // consumer-only: последний выданный кадр (хвост для затухания)
    bool mFadeIn = false;                  // consumer-only: нарастание первых кадров следующего чтения
    int64_t mPrimeStartNs = 0;             // consumer-only: начало текущего прайминга (0 = ещё не замечено)
    std::atomic<int64_t> mLastWriteNs{0};  // producer->consumer: время последнего write()
    EventRing<32> mConsumerEvents;         // писатель: колбэк Oboe; читатель: drainEvents
    EventRing<32> mProducerEvents;         // писатель: write(); читатель: drainEvents
};

#endif  // TIMELINE_BUFFER_H
