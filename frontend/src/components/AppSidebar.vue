<script setup lang="ts">
import { computed, type Component } from "vue";
import { logout, openUpdateDialog, saveSettings, state, type ViewKey } from "../store";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarSeparator,
} from "@/components/ui/sidebar";
import { ArrowUpRight, Bell, Bookmark, CalendarDays, LogOut, Moon, Settings, Sun, Zap } from "@lucide/vue";
import BrandMark from "./BrandMark.vue";

interface NavItem {
  key: ViewKey;
  label: string;
  icon: Component;
}

const nav: NavItem[] = [
  { key: "lectures", label: "讲座", icon: CalendarDays },
  { key: "grab", label: "抢座", icon: Zap },
  { key: "mine", label: "我的预约", icon: Bookmark },
  { key: "notices", label: "通知公告", icon: Bell },
  { key: "settings", label: "设置", icon: Settings },
];

const initials = computed(() => {
  const name = state.status?.user?.displayName || state.status?.user?.username || "?";
  const chars = Array.from(name);
  return /[\u3400-\u9fff]/.test(name) ? chars[0] : chars.slice(0, 2).join("").toUpperCase();
});

const displayName = computed(
  () => state.status?.user?.displayName || state.status?.user?.username || "未登录",
);

// 主题切换放在侧边栏底部: 设置页里要跳一层才能改外观, 太绕。
const light = computed(() => state.settings?.theme === "light");

async function toggleTheme(): Promise<void> {
  await saveSettings({ theme: light.value ? "dark" : "light" });
}

async function signOut(): Promise<void> {
  await logout();
}
</script>

<template>
  <Sidebar collapsible="none" class="border-r">
    <SidebarHeader class="gap-3 px-3 py-4">
      <div class="flex items-center gap-2.5">
        <BrandMark class="size-8 shrink-0" />
        <div class="min-w-0 leading-tight">
          <p class="truncate text-sm font-semibold tracking-tight">东南大学讲座助手</p>
          <p class="truncate text-[11px] text-muted-foreground">SEU Lecture</p>
        </div>
      </div>
    </SidebarHeader>

    <SidebarSeparator />

    <SidebarContent class="px-2 py-2">
      <SidebarGroup class="p-0">
        <SidebarGroupLabel>导航</SidebarGroupLabel>
        <SidebarGroupContent>
          <SidebarMenu class="gap-0.5">
            <SidebarMenuItem v-for="item in nav" :key="item.key">
              <SidebarMenuButton
                :is-active="state.view === item.key"
                class="h-9"
                @click="state.view = item.key"
              >
                <component :is="item.icon" />
                <span class="flex-1 truncate">{{ item.label }}</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarGroupContent>
      </SidebarGroup>
    </SidebarContent>

    <SidebarFooter class="gap-2 border-t px-3 py-3">
      <div class="flex items-center gap-2">
        <Avatar class="size-7 shrink-0">
          <AvatarFallback class="text-[11px] font-medium">{{ initials }}</AvatarFallback>
        </Avatar>
        <div class="min-w-0 flex-1 leading-tight">
          <p class="truncate text-xs font-medium">{{ displayName }}</p>
          <p class="truncate text-[11px] text-muted-foreground">
            {{ state.status?.user?.username || "—" }}
          </p>
        </div>
        <Button
          variant="ghost"
          size="icon-sm"
          :title="light ? '切换到深色主题' : '切换到浅色主题'"
          @click="toggleTheme"
        >
          <Sun v-if="light" />
          <Moon v-else />
        </Button>
        <Button variant="ghost" size="icon-sm" title="退出登录" @click="signOut">
          <LogOut />
        </Button>
      </div>

      <div class="flex items-center justify-between gap-2">
        <Badge variant="outline" class="font-normal">
          {{ state.status?.ocrAvailable ? "验证码识别就绪" : "手动验证码" }}
        </Badge>
        <span class="truncate text-[11px] text-muted-foreground tabular-nums">
          {{ state.status?.version || "" }}
        </span>
      </div>

      <!-- 有更新时在左下角提示；点开是版本对比与变更列表 -->
      <button
        v-if="state.update?.hasUpdate"
        type="button"
        class="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-[11px] font-medium text-foreground transition-colors hover:bg-sidebar-accent"
        @click="openUpdateDialog()"
      >
        <span class="size-1.5 shrink-0 animate-pulse rounded-full bg-emerald-500" />
        <span class="truncate">发现新版本 v{{ state.update.latest }}</span>
        <ArrowUpRight class="ml-auto size-3.5 shrink-0" />
      </button>
    </SidebarFooter>
  </Sidebar>
</template>
