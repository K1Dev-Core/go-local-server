<?php

declare(strict_types=1);

// กำหนดค่าคงที่สำหรับการอนุญาตวิธีการร้องขอต่างๆ
// ในที่นี้ เราอนุญาตเฉพาะ GET และ POST
const ALLOW_METHODS = ['GET', 'POST'];
const INDEX_URI = '';

// กำหนดค่าคงที่สำหรับ route เริ่มต้น
const INDEX_ROUNTE = 'home';


// ฟังชันสำหรับทำให้ URI ที่ร้องขอเข้ามาอยู่ในรูปแบบมาตรฐาน
function normalizeUri(string $uri): string
{
    // ลบเครื่องหมาย '/' ที่อยู่ข้างหน้าและข้างหลังออก และแปลงเป็นตัวพิมพ์เล็ก
    $uri = strtolower(trim($uri, '/'));

    // เช็คว่า URI ว่างหรือไม่ ถ้าว่างให้เปลี่ยนเป็น route เริ่มต้น
    return $uri == INDEX_URI ? INDEX_ROUNTE : $uri;
}

// ฟังชันสำหรับแสดงหน้า 404 Not Found
function notFound()
{
    http_response_code(404);
    // เรียกใช้ฟังก์ชัน renderView เพื่อแสดงหน้า 404
    renderView('404');
    exit;
}

// ฟังชันสำหรับการหาเส้นทางไฟล์ PHP ที่ตรงกับ URI ที่ร้องขอเข้ามา
function getFilePath(string $uri): string
{
    return ROUTE_DIR . '/' . normalizeUri($uri) . '.php';
}

// ฟังก์ชันหลักสำหรับการจัดการเส้นทาง (routing) ที่ถูกเรียกใช้จาก index.php
function dispatch(string $uri, string $method): void
{
    // ฟังชันสำหรับทำให้ URI ที่ร้องขอเข้ามาอยู่ในรูปแบบมาตรฐาน
    $uri = normalizeUri($uri);

    // ตรวจสอบว่าวิธีการร้องขอ (HTTP Method) ถูกอนุญาตหรือไม่
    if (!in_array(strtoupper($method), ALLOW_METHODS)) {
        notFound();
    }

    // ฟังชันสำหรับการหาเส้นทางไฟล์ PHP ที่ตรงกับ URI ที่ร้องขอเข้ามา
    $filePath = getFilePath($uri);
    if (file_exists($filePath)) {
        include($filePath);
        return;
    } else {
        notFound();
    }
}
