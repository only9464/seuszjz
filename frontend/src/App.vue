<script setup lang="ts">
import { computed, onMounted } from "vue";
import { boot, refresh, state } from "./store";
import AppSidebar from "./components/AppSidebar.vue";
import DisclaimerDialog from "./components/DisclaimerDialog.vue";
import UpdateDialog from "./components/UpdateDialog.vue";
import LoginView from "./components/views/LoginView.vue";
import LecturesView from "./components/views/LecturesView.vue";
import MineView from "./components/views/MineView.vue";
import NoticesView from "./components/views/NoticesView.vue";
import GrabView from "./components/views/GrabView.vue";
import SettingsView from "./components/views/SettingsView.vue";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { SidebarInset, SidebarProvider, SidebarTrigger } from "@/components/ui/sidebar";
import { Spinner } from "@/components/ui/spinner";
import { Toaster } from "@/components/ui/sonner";
import { RefreshCw } from "@lucide/vue";

const views = {
  lectures: LecturesView,
  grab: GrabView,
  mine: MineView,
  notices: NoticesView,
  settings: SettingsView,
} as const;

const current = computed(() => views[state.view] ?? LecturesView);

const meta = computed(() => {
  switch (state.view) {
    case "grab":
      return { title: "抢座", hint: "轮询余量并自动提交预约" };
    case "mine":
      return { title: "我的预约", hint: "已预约的讲座与现场签到" };
    case "notices":
      return { title: "通知公告", hint: "教务发布的通知与提醒" };
    case "settings":
      return { title: "设置", hint: "账号、抢座参数与外观" };
    default:
      return { title: "讲座", hint: "全校素质讲座列表" };
  }
});

const canRefresh = computed(
  () => state.status?.loggedIn && (state.view === "lectures" || state.view === "mine" || state.view === "notices"),
);

onMounted(() => {
  void boot();
});
</script>

<template>
  <div v-if="state.booting" class="flex h-full items-center justify-center bg-background">
    <div class="flex flex-col items-center gap-4 text-muted-foreground">
      <Spinner class="size-5" />
      <p class="text-xs tracking-[0.2em] uppercase">正在连接本地服务</p>
    </div>
  </div>

  <LoginView v-else-if="!state.status?.loggedIn" />

  <SidebarProvider v-else class="h-full overflow-hidden">
    <AppSidebar />

    <SidebarInset class="h-full min-w-0 overflow-hidden">
      <header class="flex h-14 shrink-0 items-center gap-3 border-b px-4">
        <SidebarTrigger class="-ml-1.5" />
        <div class="min-w-0 flex-1 leading-tight">
          <h1 class="truncate text-sm font-semibold tracking-tight">{{ meta.title }}</h1>
          <p class="truncate text-xs text-muted-foreground">{{ meta.hint }}</p>
        </div>

        <Badge v-if="state.grab?.running" variant="outline" class="gap-1.5">
          <span class="size-1.5 rounded-full bg-foreground animate-pulse" />
          抢座中
        </Badge>

        <Button
          v-if="canRefresh"
          variant="ghost"
          size="sm"
          :disabled="state.loadingList"
          @click="refresh()"
        >
          <RefreshCw :class="state.loadingList ? 'animate-spin' : ''" />
          刷新
        </Button>
      </header>

      <div class="relative min-h-0 flex-1">
        <Transition name="fade" mode="out-in">
          <component :is="current" :key="state.view" class="absolute inset-0" />
        </Transition>
      </div>
    </SidebarInset>
  </SidebarProvider>

  <Toaster position="bottom-right" close-button />

  <!-- 首次启动的免责声明（勾选后才能继续）与更新详情 -->
  <DisclaimerDialog />
  <UpdateDialog />
</template>
