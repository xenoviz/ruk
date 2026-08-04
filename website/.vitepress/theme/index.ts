import { h, onMounted, watch } from "vue";
import { useRoute } from "vitepress";
import DefaultTheme from "vitepress/theme";
import "./custom.css";

const Layout = {
  setup() {
    const route = useRoute();
    const markHomeMain = () => {
      requestAnimationFrame(() => document.querySelector(".VPHome")?.setAttribute("role", "main"));
    };
    onMounted(markHomeMain);
    watch(() => route.path, markHomeMain);
    return () => h(DefaultTheme.Layout);
  },
};

export default {
  extends: DefaultTheme,
  Layout,
};
