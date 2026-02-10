<?php

declare(strict_types=1);

// ฟังก์ชันสำหรับแสดงมุมมอง (view) โดยรับชื่อเทมเพลตและข้อมูลที่ต้องการส่งไปยังเทมเพลต
function renderView(string $template, array $data = []): void
{
    include TEMPLATES_DIR . '/' . $template . '.php';
}
