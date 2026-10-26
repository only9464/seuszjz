<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { forgetLogin, logout, saveSettings, state } from "../../store";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
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
import { Switch } from "@/components/ui/switch";
import { LogOut, Trash2 } from "@lucide/vue";

const settings = computed(() => state.settings);

const remember = computed({
  get: () => !!settings.value?.remember,
  set: (value: boolean) => void saveSettings({ remember: value }),
});

const autoLogin = computed({
  get: () => !!settings.value?.autoLogin,
  set: (value: boolean) => void saveSettings({ autoLogin: value }),
});

const netMode = computed({
  get: () => settings.value?.netMode || "auto",
  set: (value: string) => void saveSettings({ netMode: value }),
});

const autoCaptcha = computed({
  get: () => !!settings.value?.autoCaptcha,
  set: (value: boolean) => void saveSettings({ autoCaptcha: value }),
});



const interval = ref(settings.value?.interval ?? 3);
const cooldown = ref(settings.value?.cooldown ?? 300);

watch(settings, (value) => {
  if (!value) return;
  interval.value = value.interval;
  cooldown.value = value.cooldown;
});

function saveInterval(): void {
  if (!settings.value || interval.value === settings.value.interval) return;
  void saveSettings({ interval: interval.value });
}

function saveCooldown(): void {
  if (!settings.value || cooldown.value === settings.value.cooldown) return;
  void saveSettings({ cooldown: cooldown.value });
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <ScrollArea class="min-h-0 flex-1">
      <div class="mx-auto grid w-full max-w-3xl auto-rows-min items-start gap-4 p-4 md:grid-cols-2">
        <Card class="gap-0 py-0">
          <CardHeader class="px-4 pt-4 pb-2">
            <CardTitle class="text-sm">账号</CardTitle>
            <CardDescription class="text-xs">
              {{ settings?.username || "未登录" }}
              <Badge v-if="settings?.hasPassword" variant="outline" class="ml-2">已保存密码</Badge>
            </CardDescription>
          </CardHeader>
          <CardContent class="space-y-2 px-4 pb-4">
            <div class="flex items-center justify-between gap-4 rounded-md border px-3 py-2.5">
              <div class="space-y-0.5">
                <p class="text-xs font-medium">记住登录信息</p>
                <p class="text-[11px] text-muted-foreground">学号、会话 Cookie 与混淆后的密码</p>
              </div>
              <Switch v-model="remember" />
            </div>

            <div class="flex items-center justify-between gap-4 rounded-md border px-3 py-2.5">
              <div class="space-y-0.5">
                <p class="text-xs font-medium">启动时免登录</p>
                <p class="text-[11px] text-muted-foreground">复用已保存的会话，失效时自动重登</p>
              </div>
              <Switch v-model="autoLogin" />
            </div>

            <div class="flex items-center justify-between gap-4 rounded-md border px-3 py-2.5">
              <div class="space-y-0.5">
                <p class="text-xs font-medium">网络环境</p>
                <p class="text-[11px] text-muted-foreground">
                  校园网下直连 ehall，跳过 aTrust 网关
                </p>
              </div>
              <Select v-model="netMode">
                <SelectTrigger class="h-8 w-28 text-xs" size="sm">
                  <SelectValue placeholder="自动识别" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="auto">自动识别</SelectItem>
                  <SelectItem value="campus">校园网</SelectItem>
                  <SelectItem value="offcampus">校外</SelectItem>
                </SelectContent>
              </Select>
            </div>

            <Separator class="my-1" />

            <div class="flex flex-wrap gap-2">
              <Button variant="outline" size="sm" @click="forgetLogin()">
                <Trash2 />
                清除本地登录信息
              </Button>
              <Button variant="ghost" size="sm" @click="logout()">
                <LogOut />
                退出登录
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card class="gap-0 py-0">
          <CardHeader class="px-4 pt-4 pb-2">
            <CardTitle class="text-sm">抢座</CardTitle>
            <CardDescription class="text-xs">轮询间隔与失败冷却</CardDescription>
          </CardHeader>
          <CardContent class="space-y-2 px-4 pb-4">
            <div class="flex items-center justify-between gap-4 rounded-md border px-3 py-2.5">
              <div class="space-y-0.5">
                <p class="text-xs font-medium">自动识别验证码</p>
                <p class="text-[11px] text-muted-foreground">关闭后每张验证码手动输入</p>
              </div>
              <Switch v-model="autoCaptcha" />
            </div>

            <div class="flex items-center justify-between gap-4 rounded-md border px-3 py-2.5">
              <div class="space-y-0.5">
                <Label for="interval" class="text-xs font-medium">轮询间隔（秒）</Label>
                <p class="text-[11px] text-muted-foreground">两次查询之间等待多久</p>
              </div>
              <Input
                id="interval"
                v-model.number="interval"
                type="number"
                min="1"
                class="h-8 w-20 text-xs tabular-nums"
                @blur="saveInterval"
              />
            </div>

            <div class="flex items-center justify-between gap-4 rounded-md border px-3 py-2.5">
              <div class="space-y-0.5">
                <Label for="cooldown" class="text-xs font-medium">冷却时间（秒）</Label>
                <p class="text-[11px] text-muted-foreground">触发限流后暂停多久</p>
              </div>
              <Input
                id="cooldown"
                v-model.number="cooldown"
                type="number"
                min="10"
                class="h-8 w-20 text-xs tabular-nums"
                @blur="saveCooldown"
              />
            </div>
          </CardContent>
        </Card>



        <Card class="gap-0 py-0">
          <CardHeader class="px-4 pt-4 pb-2">
            <CardTitle class="text-sm">本地数据与识别</CardTitle>
            <CardDescription class="text-xs">程序目录里不保存任何账号文件</CardDescription>
          </CardHeader>
          <CardContent class="space-y-2 px-4 pb-4">
            <div class="flex items-start justify-between gap-4 rounded-md border px-3 py-2.5">
              <div class="space-y-0.5">
                <p class="text-xs font-medium">登录信息</p>
                <p class="text-[11px] leading-relaxed text-muted-foreground">
                  存于浏览器 localStorage（seuszjz.state.v1），密码做 AES-GCM 混淆；清除后需要重新登录。
                </p>
              </div>
              <Badge variant="outline" class="shrink-0">{{ settings?.hasPassword ? "已保存" : "未保存" }}</Badge>
            </div>

            <div class="flex items-start justify-between gap-4 rounded-md border px-3 py-2.5">
              <div class="space-y-0.5">
                <p class="text-xs font-medium">验证码识别</p>
                <p class="text-[11px] leading-relaxed text-muted-foreground">
                  {{ state.status?.ocrInfo || "内置 ddddocr 模型，无需联网" }}
                </p>
              </div>
              <Badge :variant="state.status?.ocrAvailable ? 'default' : 'outline'" class="shrink-0">
                {{ state.status?.ocrAvailable ? "就绪" : "不可用" }}
              </Badge>
            </div>

            <div class="flex items-center justify-between gap-4 rounded-md border px-3 py-2.5">
              <p class="text-xs font-medium">版本</p>
              <span class="font-mono text-[11px] text-muted-foreground">{{ state.status?.version || "dev" }}</span>
            </div>
          </CardContent>
        </Card>
      </div>
    </ScrollArea>
  </div>
</template>
