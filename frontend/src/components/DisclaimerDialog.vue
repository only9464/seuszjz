<script setup lang="ts">
import { ref } from "vue";
import { acceptDisclaimer, openReleases, REPO_URL, state } from "../store";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import warnImage from "../assets/warn.png";

// 两页：第 1 页是学校通知截图（必须先看），第 2 页才是免责声明与勾选。
const page = ref(0);
const checked = ref(false);

function next(): void {
  page.value = 1;
}

function confirm(): void {
  if (!checked.value) return;
  acceptDisclaimer();
}
</script>

<template>
  <Dialog v-model:open="state.disclaimerOpen">
    <!-- 必须走完两页并勾选才能继续：点遮罩、按 Esc 都不关闭，也不给关闭按钮。 -->
    <DialogContent
      class="max-h-[86vh] overflow-y-auto sm:max-w-xl"
      :show-close-button="false"
      @interact-outside.prevent
      @escape-key-down.prevent
    >
      <DialogHeader>
        <DialogTitle>{{ page === 0 ? "重要提醒" : "使用前请阅读" }}</DialogTitle>
        <DialogDescription>
          {{
            page === 0
              ? "学校已明确禁止使用脚本预约讲座，请先看完这一页。"
              : "这是一个免费的开源项目，与东南大学官方无关。"
          }}
        </DialogDescription>
      </DialogHeader>

      <!-- 第 1 页：学校通知原文截图 -->
      <div v-if="page === 0" class="space-y-3">
        <!-- 高度设上限: 通知截图很长, 限高后「我已阅读，下一步」不会被挤到屏幕外 -->
        <div class="flex justify-center">
          <img
            :src="warnImage"
            alt="学校关于禁止使用脚本预约讲座的通知截图"
            class="max-h-[46vh] w-auto rounded-lg border bg-white"
          />
        </div>
        <p class="text-xs leading-relaxed text-muted-foreground">
          以上为学校通知原文：严禁使用脚本预约讲座、严禁售卖或购买预约脚本、严禁委托他人代听。
          一经核实，系统将限制预约权限并依规处置。
        </p>
      </div>

      <!-- 第 2 页：免责声明 -->
      <div v-else class="space-y-3 text-sm leading-relaxed break-words text-muted-foreground">
        <p>
          本项目以 MIT 许可证开源，全部代码公开在
          <a
            :href="REPO_URL"
            target="_blank"
            rel="noreferrer"
            class="font-medium break-all text-foreground underline underline-offset-2"
            @click.prevent="openReleases()"
          >{{ REPO_URL }}</a>
          ，任何人都可以查阅、复制与修改；项目不收费、不含广告，也不收集任何个人数据。
        </p>

        <div class="rounded-lg border bg-muted/40 p-3">
          <p class="mb-1 text-xs font-medium text-foreground">免责声明</p>
          <p class="text-xs leading-relaxed">
            本项目仅用于个人预约讲座的自动化尝试，与东南大学官方无关。请遵守学校相关规定，不要用于抢票、代约等违规用途；使用本工具产生的一切后果由使用者自行承担。
          </p>
        </div>

        <p class="text-xs">
          账号、密码（混淆后）与会话 Cookie 只保存在你自己浏览器的本地存储里，程序不会上传到任何第三方服务器。
        </p>
      </div>

      <label
        v-if="page === 1"
        class="flex cursor-pointer items-start gap-2.5 text-sm leading-relaxed"
      >
        <input
          v-model="checked"
          type="checkbox"
          class="mt-0.5 size-4 shrink-0 cursor-pointer accent-foreground"
        />
        <span>我已认真阅读以上内容，并承诺本人所有行为产生的后果自行承担</span>
      </label>

      <DialogFooter class="items-center gap-2 sm:justify-between">
        <span class="text-[11px] text-muted-foreground">{{ page + 1 }} / 2</span>
        <Button v-if="page === 0" class="w-full sm:w-auto" @click="next">
          我已阅读，下一步
        </Button>
        <Button v-else class="w-full sm:w-auto" :disabled="!checked" @click="confirm">
          同意并继续
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>