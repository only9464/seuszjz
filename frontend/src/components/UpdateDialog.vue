<script setup lang="ts">
import { computed } from "vue";
import { checkUpdate, openReleases, state } from "../store";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Spinner } from "@/components/ui/spinner";
import { ArrowUpRight, Link2 } from "@lucide/vue";

const info = computed(() => state.update);

const current = computed(() => info.value?.current || state.status?.version || "—");
const latest = computed(() => info.value?.latest || "—");
const notes = computed(() => info.value?.notes ?? []);
const error = computed(() => info.value?.error || "");
const hasUpdate = computed(() => !!info.value?.hasUpdate);

function close(): void {
  state.updateOpen = false;
}
</script>

<template>
  <Dialog v-model:open="state.updateOpen">
    <DialogContent class="max-h-[86vh] overflow-y-auto sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>
          {{ hasUpdate ? `发现新版本 v${latest}` : "检查更新" }}
        </DialogTitle>
        <DialogDescription>
          只读取 GitHub 上的发布信息，不会自动下载或替换文件。
        </DialogDescription>
      </DialogHeader>

      <div class="grid grid-cols-2 gap-2 text-sm">
        <div class="rounded-lg border px-3 py-2">
          <p class="text-[11px] text-muted-foreground">当前版本</p>
          <p class="font-medium tabular-nums">v{{ current }}</p>
        </div>
        <div class="rounded-lg border px-3 py-2">
          <p class="text-[11px] text-muted-foreground">最新版本</p>
          <p class="font-medium tabular-nums">
            <span v-if="hasUpdate" class="text-foreground">v{{ latest }}</span>
            <span v-else>{{ latest === "—" ? "—" : `v${latest}` }}</span>
          </p>
        </div>
      </div>

      <p v-if="error" class="text-xs text-muted-foreground">
        检查失败：{{ error }}。校园网访问 GitHub 可能不稳定，可以稍后再试。
      </p>

      <div v-else-if="notes.length" class="space-y-2">
        <p class="text-xs font-medium text-foreground">更新内容</p>
        <ul class="space-y-1.5 text-xs leading-relaxed text-muted-foreground">
          <li v-for="(note, index) in notes" :key="index" class="flex gap-2">
            <span class="mt-1 size-1 shrink-0 rounded-full bg-muted-foreground/60" />
            <span>{{ note }}</span>
          </li>
        </ul>
      </div>

      <p v-else-if="hasUpdate" class="text-xs text-muted-foreground">
        该版本没有附带变更说明，可以到发布页查看完整内容。
      </p>

      <p v-else class="text-xs text-muted-foreground">
        当前已是最新版本。
      </p>

      <div class="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
        <Link2 class="size-3.5 shrink-0" />
        <a
          href="https://github.com/only9464/seuszjz/releases/latest"
          target="_blank"
          rel="noreferrer"
          class="min-w-0 truncate underline underline-offset-2 hover:text-foreground"
          @click.prevent="openReleases()"
        >github.com/only9464/seuszjz/releases/latest</a>
      </div>

      <DialogFooter class="gap-2 sm:justify-between">
        <Button variant="ghost" size="sm" :disabled="state.checkingUpdate" @click="checkUpdate(false)">
          <Spinner v-if="state.checkingUpdate" class="size-3.5" />
          重新检查
        </Button>
        <div class="flex gap-2">
          <Button variant="outline" size="sm" @click="close">稍后再说</Button>
          <Button size="sm" @click="openReleases()">
            打开发布页
            <ArrowUpRight />
          </Button>
        </div>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
