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

const checked = ref(false);

function confirm(): void {
  if (!checked.value) return;
  acceptDisclaimer();
}
</script>

<template>
  <Dialog v-model:open="state.disclaimerOpen">
    <!-- 必须勾选才能继续：点遮罩、按 Esc 都不关闭，也不给关闭按钮。 -->
    <DialogContent
      class="max-h-[86vh] overflow-y-auto sm:max-w-xl"
      :show-close-button="false"
      @interact-outside.prevent
      @escape-key-down.prevent
    >
      <DialogHeader>
        <DialogTitle>使用前请阅读</DialogTitle>
        <DialogDescription>
          这是一个免费的开源项目，与东南大学官方无关。
        </DialogDescription>
      </DialogHeader>

      <div class="space-y-3 text-sm leading-relaxed text-muted-foreground">
        <p>
          本项目以 MIT 许可证开源，全部代码公开在
          <a
            :href="REPO_URL"
            target="_blank"
            rel="noreferrer"
            class="font-medium text-foreground underline underline-offset-2"
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

      <label class="flex cursor-pointer items-start gap-2.5 text-sm leading-relaxed">
        <input
          v-model="checked"
          type="checkbox"
          class="mt-0.5 size-4 shrink-0 cursor-pointer accent-foreground"
        />
        <span>我已认真阅读以上内容，并承诺本人所有行为产生的后果自行承担</span>
      </label>

      <DialogFooter>
        <Button class="w-full sm:w-auto" :disabled="!checked" @click="confirm">
          同意并继续
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
