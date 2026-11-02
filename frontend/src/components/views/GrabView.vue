<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import {
  clearGrabLogs,
  clockOf,
  formatDateTime,
  refreshGrabCaptcha,
  saveSettings,
  setGrabTarget,
  startGrab,
  state,
  stopGrab,
  submitGrabCode,
} from "../../store";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Play, RefreshCw, Square, Zap } from "@lucide/vue";

const settings = state.settings;

// 选择器的取值以「用户选的目标」为准。
// 之前 get 优先取 state.grab?.wid —— 停止后那个快照还在, 于是下拉框永远显示上一次的讲座,
// 选别的会被立刻顶回去, 看起来就像「停止后没法切换目标」。
const selected = computed({
  get: () => state.grabTarget || state.grab?.wid || "",
  set: (value: string) => {
    // 抢座进行中也允许切换: 后端会在下一轮轮询改用新目标 (不用先停止重开)。
    // 顺带把讲座名传给后端, 日志里就能看到「已切换抢座目标: xxx」。
    const lecture = state.lectures.find((item) => item.wid === value);
    void setGrabTarget(value, lecture?.name ?? "");
  },
});

const waitMs = ref(settings?.waitMs ?? 1000);
const attemptMs = ref(settings?.attemptMs ?? 300);
const cooldown = ref(settings?.cooldown ?? 300);
const autoCaptcha = ref(settings?.autoCaptcha ?? true);

const grabbing = computed(() => !!state.grab?.running);
const captcha = computed(() => state.grab?.captcha ?? "");
const code = ref("");
const submitting = ref(false);

const logs = computed(() => state.grab?.logs ?? []);
const dropped = computed(() => state.grab?.logsDropped ?? 0);
const logBox = ref<HTMLElement | null>(null);
// 自动滚动: 用户往上翻看历史时不要把他拽回底部。
const follow = ref(true);

function onLogScroll(): void {
  const el = logBox.value;
  if (!el) return;
  follow.value = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
}

watch(
  () => logs.value.length,
  async () => {
    if (!follow.value) return;
    await nextTick();
    const el = logBox.value;
    if (el) el.scrollTop = el.scrollHeight;
  },
);

function begin(): void {
  if (!selected.value) return;
  void saveSettings({
    waitMs: waitMs.value,
    attemptMs: attemptMs.value,
    cooldown: cooldown.value,
    autoCaptcha: autoCaptcha.value,
  });
  startGrab(selected.value, waitMs.value, attemptMs.value, cooldown.value, autoCaptcha.value);
}

async function submit(): Promise<void> {
  if (!code.value.trim()) return;
  submitting.value = true;
  try {
    const ok = await submitGrabCode(code.value.trim());
    if (ok) code.value = "";
  } finally {
    submitting.value = false;
  }
}

function levelClass(level: string): string {
  switch (level) {
    case "error":
      return "text-destructive";
    case "warn":
      return "text-muted-foreground";
    case "ok":
      return "text-foreground";
    default:
      return "text-muted-foreground/80";
  }
}

const phaseText = computed(() => {
  const phase = state.grab?.phase ?? "";
  switch (phase) {
    case "idle":
      return "空闲";
    case "waiting":
      return "等待开放";
    case "running":
      return "抢座中";
    case "captcha":
      return "等待验证码";
    case "done":
      return "已完成";
    default:
      return phase || "空闲";
  }
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <div class="shrink-0 space-y-3 border-b px-4 py-3">
      <div class="flex flex-wrap items-end gap-2">
        <div class="min-w-0 flex-1 space-y-1.5">
          <Label class="text-[11px] text-muted-foreground">抢座目标</Label>
          <Select v-model="selected">
            <SelectTrigger class="h-8 w-full text-xs" size="sm">
              <SelectValue placeholder="选择一讲讲座" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="lecture in state.lectures" :key="lecture.wid" :value="lecture.wid">
                {{ lecture.name }}
              </SelectItem>
            </SelectContent>
          </Select>
        </div>

        <Button v-if="!grabbing" size="sm" :disabled="!selected" @click="begin">
          <Play />
          开始抢座
        </Button>
        <Button v-else variant="outline" size="sm" @click="stopGrab()">
          <Square />
          停止
        </Button>
      </div>

      <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div class="flex items-center gap-2">
          <Label for="waitMs" class="text-[11px] font-normal text-muted-foreground">等待轮询</Label>
          <Input
            id="waitMs"
            v-model.number="waitMs"
            type="number"
            min="200"
            step="100"
            class="h-7 w-20 text-xs tabular-nums"
            :disabled="grabbing"
          />
          <span class="text-[11px] text-muted-foreground">ms</span>
        </div>

        <div class="flex items-center gap-2">
          <Label for="attemptMs" class="text-[11px] font-normal text-muted-foreground">提交间隔</Label>
          <Input
            id="attemptMs"
            v-model.number="attemptMs"
            type="number"
            min="50"
            step="50"
            class="h-7 w-20 text-xs tabular-nums"
            :disabled="grabbing"
          />
          <span class="text-[11px] text-muted-foreground">ms</span>
        </div>

        <div class="flex items-center gap-2">
          <Label for="cooldown" class="text-[11px] font-normal text-muted-foreground">限流冷却</Label>
          <Input
            id="cooldown"
            v-model.number="cooldown"
            type="number"
            min="0"
            class="h-7 w-20 text-xs tabular-nums"
            :disabled="grabbing"
          />
          <span class="text-[11px] text-muted-foreground">秒</span>
        </div>

        <Separator orientation="vertical" class="h-4!" />

        <label class="flex items-center gap-2 text-[11px] text-muted-foreground">
          <Switch v-model="autoCaptcha" :disabled="grabbing" />
          自动识别验证码
        </label>

        <Separator orientation="vertical" class="h-4!" />

        <p class="text-[11px] text-muted-foreground tabular-nums">
          轮询 {{ state.grab?.round ?? 0 }} 轮 · 提交 {{ state.grab?.attempts ?? 0 }} 次 · 验证码
          {{ state.grab?.captchaCount ?? 0 }} 张
        </p>
        <p v-if="state.grab?.startedAt" class="text-[11px] text-muted-foreground tabular-nums">
          始于 {{ formatDateTime(state.grab.startedAt) }}
        </p>
      </div>
    </div>

    <ScrollArea class="min-h-0 flex-1">
      <div class="space-y-4 p-4">
        <div class="flex items-start gap-3 rounded-lg border bg-card p-4">
          <div class="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-md border bg-muted">
            <Spinner v-if="grabbing" class="size-3.5" />
            <Zap v-else class="size-3.5 text-muted-foreground" />
          </div>
          <div class="min-w-0 flex-1 space-y-1">
            <div class="flex flex-wrap items-center gap-2">
              <Badge :variant="state.grab?.success ? 'default' : 'outline'">{{ phaseText }}</Badge>
              <span class="truncate text-xs font-medium">{{ state.grab?.name || "尚未选择讲座" }}</span>
            </div>
            <p class="text-xs leading-relaxed text-muted-foreground">
              {{ state.grab?.message || "选择一讲讲座后点击开始，程序会按间隔轮询余量并自动提交预约。" }}
            </p>
          </div>
        </div>

        <div v-if="captcha" class="rounded-lg border bg-card p-4">
          <div class="mb-3 flex items-center justify-between">
            <p class="text-xs font-medium">需要手动输入验证码</p>
            <Button variant="ghost" size="sm" @click="refreshGrabCaptcha()">
              <RefreshCw />
              换一张
            </Button>
          </div>
          <div class="flex flex-wrap items-center gap-3">
            <img
              :src="captcha"
              alt="验证码"
              class="h-11 w-auto rounded-md border bg-white object-contain px-2"
            />
            <Input
              v-model="code"
              class="h-9 w-32 text-sm tracking-[0.3em] tabular-nums"
              placeholder="验证码"
              maxlength="8"
              @keyup.enter="submit"
            />
            <Button size="sm" :disabled="submitting || !code.trim()" @click="submit">
              <Spinner v-if="submitting" />
              提交
            </Button>
          </div>
        </div>

        <div class="rounded-lg border bg-card">
          <div class="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2.5">
            <div class="flex items-center gap-2">
              <p class="text-xs font-medium">运行日志</p>
              <p class="text-[11px] text-muted-foreground tabular-nums">
                {{ logs.length }} 条
                <span v-if="dropped"> · 已自动清屏 {{ dropped }} 条</span>
              </p>
            </div>
            <div class="flex items-center gap-2">
              <label class="flex items-center gap-1.5 text-[11px] text-muted-foreground">
                <Switch v-model="follow" />
                自动滚动
              </label>
              <Button variant="ghost" size="sm" :disabled="!logs.length" @click="clearGrabLogs()">
                清屏
              </Button>
            </div>
          </div>
          <div
            ref="logBox"
            class="max-h-[420px] min-h-32 overflow-auto px-4 py-3"
            @scroll.passive="onLogScroll"
          >
            <p v-if="!logs.length" class="py-8 text-center text-[11px] text-muted-foreground">
              还没有日志，启动抢座后这里会实时输出进度。
            </p>
            <ul v-else class="space-y-1 font-mono text-[11px] leading-relaxed">
              <li v-for="(entry, index) in logs" :key="index" class="flex gap-2">
                <span class="shrink-0 text-muted-foreground/60 tabular-nums">{{ clockOf(entry.at) }}</span>
                <span :class="levelClass(entry.level)" class="break-all">{{ entry.text }}</span>
              </li>
            </ul>
          </div>
        </div>
      </div>
    </ScrollArea>

    <!-- 常驻提醒: 双设备限制是最容易踩的坑, 放在底部固定可见 -->
    <div class="shrink-0 border-t border-status-full-border bg-status-full/50 px-4 py-2.5">
      <div class="flex items-start gap-2">
        <svg
          class="mt-0.5 size-3.5 shrink-0 text-status-full-fg"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
        >
          <path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0Z" />
          <line x1="12" y1="9" x2="12" y2="13" />
          <line x1="12" y1="17" x2="12.01" y2="17" />
        </svg>
        <div class="min-w-0 flex-1 space-y-0.5">
          <p class="text-xs font-medium text-status-full-fg">
            抢座期间请勿在手机 App 或其他设备上打开「素质讲座」页面
          </p>
          <p class="text-[11px] leading-relaxed text-muted-foreground">
            平台对同一账号有双设备限制：手机、浏览器与本程序同时访问会把当前会话顶下线，
            可能导致抢座中断、预约失败甚至触发风控。
            <span class="whitespace-nowrap text-muted-foreground/75" title="双设备限制提示来自网友 momo 的反馈">—@momo</span>
          </p>
        </div>
        <span
          v-if="grabbing"
          class="shrink-0 rounded border border-status-full-border bg-status-full px-1.5 py-0.5 text-[11px] font-medium text-status-full-fg"
        >
          抢座进行中
        </span>
      </div>
    </div>
  </div>
</template>
