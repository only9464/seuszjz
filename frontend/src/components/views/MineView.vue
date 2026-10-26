<script setup lang="ts">
import { computed } from "vue";
import { cancel, check, clockOf, formatDateTime, isBusy, state } from "../../store";
import StatusBadge from "../StatusBadge.vue";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Spinner } from "@/components/ui/spinner";
import { Bookmark, CircleCheck, X } from "@lucide/vue";

const records = computed(() => state.mine);

// 毕业要求: 素养系列讲座满 8 次, 且法律 / 艺术 / 心理健康 各 1 次。
// 口径由后端 EvaluateRequirement 决定 (只算已打卡、只算素养系列)。
const requirement = computed(() => state.requirement);

const percent = computed(() => {
  const req = requirement.value;
  if (!req || req.totalNeed <= 0) return 0;
  return Math.min(100, Math.round((req.totalDone / req.totalNeed) * 100));
});

// summary 一句话说清还差什么, 达标时给出结论。
const summary = computed(() => {
  const req = requirement.value;
  if (!req) return "";
  const categories = req.categories ?? [];
  if (req.totalDone >= req.totalNeed && categories.every((c) => c.missing === 0)) {
    return "8 次与三类必修均已满足";
  }
  const parts: string[] = [];
  if (req.totalMissing > 0) parts.push(`还差 ${req.totalMissing} 次`);
  const missing = categories.filter((c) => c.missing > 0).map((c) => c.name);
  if (missing.length) parts.push(`缺 ${missing.join("、")}`);
  return parts.join("；");
});



function canCancel(status: string): boolean {
  return status !== "checked_in" && status !== "cancelled";
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <div class="flex shrink-0 items-center gap-2 border-b px-4 py-3">
      <p class="text-[11px] text-muted-foreground tabular-nums">
        共 {{ records.length }} 条预约记录
        <template v-if="state.fetchedAt"> · {{ clockOf(state.fetchedAt) }} 更新</template>
      </p>
    </div>

    <ScrollArea class="min-h-0 flex-1">
      <div
        v-if="requirement"
class="mx-4 mt-4 rounded-lg border border-requirement-panel-border bg-requirement-panel p-4"
      >
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="flex flex-wrap items-baseline gap-2">
            <span class="text-sm font-medium">毕业要求</span>
            <span class="text-[11px] text-muted-foreground">
              中期考核前 · 人文与科学素养系列讲座
            </span>
          </div>
          <span class="text-xs font-medium tabular-nums">
            {{ requirement.totalDone }} / {{ requirement.totalNeed }} 次
          </span>
        </div>

<div class="mt-3 h-1.5 w-full overflow-hidden rounded-full bg-background">
          <div
            class="h-full rounded-full transition-all"
            :class="requirement.totalDone >= requirement.totalNeed ? 'bg-status-open-fg' : 'bg-foreground'"
            :style="{ width: percent + '%' }"
          />
        </div>

        <div class="mt-3 flex flex-wrap items-center gap-2">
          <span
            v-for="item in requirement.categories ?? []"
            :key="item.name"
            class="inline-flex w-fit shrink-0 items-center gap-1 rounded-md border px-2 py-0.5 text-xs font-medium whitespace-nowrap"
            :class="
              item.missing > 0
                ? 'border-status-full-border bg-status-full text-status-full-fg'
                : 'border-status-open-border bg-status-open text-status-open-fg'
            "
          >
            <CircleCheck v-if="item.missing === 0" class="size-3" />
            {{ item.name }} {{ item.done }}/{{ item.need }}
          </span>
          <span
            class="text-[11px]"
            :class="summary.startsWith('8 次') ? 'text-status-open-fg font-medium' : 'text-muted-foreground'"
          >
            {{ summary }}
          </span>
        </div>

        <p
          v-if="requirement.pending || requirement.defaulted || requirement.ignored"
          class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground"
        >
          <span v-if="requirement.pending" class="tabular-nums">已约待打卡 {{ requirement.pending }} 场</span>
          <span v-if="requirement.defaulted" class="tabular-nums text-destructive">违约 {{ requirement.defaulted }} 次</span>
          <span v-if="requirement.ignored" class="tabular-nums">
            非素养系列 {{ requirement.ignored }} 场（不计入）
          </span>
        </p>
      </div>

      <div v-if="state.loadingList && !records.length" class="flex items-center justify-center gap-2 py-24 text-xs text-muted-foreground">
        <Spinner class="size-3.5" />
        正在加载预约记录…
      </div>

      <div v-else-if="!records.length" class="p-6">
        <Empty class="border border-dashed">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <Bookmark />
            </EmptyMedia>
            <EmptyTitle>还没有预约记录</EmptyTitle>
            <EmptyDescription>去「讲座」页挑一讲，预约成功后会出现在这里。</EmptyDescription>
          </EmptyHeader>
        </Empty>
      </div>

      <div v-else class="space-y-2 p-4">
        <article
          v-for="record in records"
          :key="record.recordWid || record.wid"
          class="flex flex-col gap-3 rounded-lg border bg-card p-4 sm:flex-row sm:items-center sm:gap-6"
        >
          <div class="min-w-0 flex-1 space-y-1.5">
            <div class="flex flex-wrap items-center gap-2">
              <StatusBadge :status="record.status" />
              <span class="text-[11px] text-muted-foreground">{{ record.series || "素质讲座" }}</span>
            </div>
            <h2 class="truncate text-sm font-medium">{{ record.name }}</h2>
            <p class="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
              <span>{{ record.speaker || "待定" }}</span>
              <span>·</span>
              <span>{{ record.venue || "地点待定" }}</span>
              <span>·</span>
              <span class="tabular-nums">{{ formatDateTime(record.startAt) }}</span>
            </p>
            <p class="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
              <span v-if="record.bookedAt" class="tabular-nums">预约于 {{ formatDateTime(record.bookedAt) }}</span>
              <span v-if="record.code" class="tabular-nums">
                现场码 <span class="font-mono text-foreground">{{ record.code }}</span>
              </span>
              <span v-if="record.checkedIn" class="inline-flex items-center gap-1">
                <CircleCheck class="size-3" />
                已打卡{{ record.checkInAt ? ` ${formatDateTime(record.checkInAt)}` : "" }}
              </span>
            </p>
          </div>

          <div class="flex shrink-0 items-center gap-2">
            <Button
              v-if="!record.checkedIn && canCancel(record.status)"
              variant="outline"
              size="sm"
              :disabled="isBusy('check-' + record.wid)"
              @click="check(record.wid)"
            >
              <Spinner v-if="isBusy('check-' + record.wid)" />
              校验
            </Button>
            <Button
              v-if="canCancel(record.status)"
              variant="ghost"
              size="sm"
              :disabled="isBusy('cancel-' + record.wid)"
              @click="cancel(record.wid)"
            >
              <Spinner v-if="isBusy('cancel-' + record.wid)" />
              <X v-else />
              取消
            </Button>
          </div>
        </article>
      </div>
    </ScrollArea>
  </div>
</template>
